package tiers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"vidara-api/models"
)

const (
	// appURLWorker is the default Worker API endpoint used to discover the Pinggy URL.
	appURLWorker          = "https://pinggy-registry.kineflex-netflex.workers.dev/api/app"
	workerRefreshBefore   = 5 * time.Minute
	workerCacheDuration   = 5 * time.Minute
	defaultTimeoutSeconds = 20
	extractorUserAgent    = "vidara-api/1.0"
)

// workerURLResponse models the JSON returned by the Worker API.
type workerURLResponse struct {
	URL       string `json:"url"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
}

// ExtractorTier represents Tier 2 resolver connecting to VidFast Extractor API via a dynamic Pinggy URL.
type ExtractorTier struct {
	extractorURL        string // Deprecated / fallback: tracks last known active Pinggy extractor URL
	workerURL           string // Worker endpoint used to discover active Pinggy URL (APP_URL_WORKER)
	vidfastBaseURL      string
	timeoutSeconds      int
	client              *http.Client
	workerRefreshBefore time.Duration
	workerCacheDuration time.Duration

	cacheMu             sync.RWMutex
	currentExtractorURL string
	extractorExpiresAt  time.Time
}

// NewExtractorTier creates a new Tier 2 extractor resolver.
// workerURL can be passed directly, or will be read from APP_URL_WORKER environment variable,
// or falls back to appURLWorker default.
func NewExtractorTier(workerURL, vidfastBaseURL string, timeout time.Duration) *ExtractorTier {
	if workerURL == "" {
		workerURL = os.Getenv("APP_URL_WORKER")
	}
	if workerURL == "" {
		workerURL = appURLWorker
	}
	if vidfastBaseURL == "" {
		vidfastBaseURL = "https://cinesrc.st/embed"
	}

	clientTimeout := timeout
	if clientTimeout < 25*time.Second {
		clientTimeout = 25 * time.Second
	}

	return &ExtractorTier{
		workerURL:           strings.TrimRight(strings.TrimSpace(workerURL), "/"),
		vidfastBaseURL:      strings.TrimRight(strings.TrimSpace(vidfastBaseURL), "/"),
		timeoutSeconds:      defaultTimeoutSeconds,
		client:              &http.Client{Timeout: clientTimeout},
		workerRefreshBefore: workerRefreshBefore,
		workerCacheDuration: workerCacheDuration,
	}
}

// GetCachedExtractorURL returns the currently cached extractor URL and its expiration time.
func (t *ExtractorTier) GetCachedExtractorURL() (string, time.Time) {
	t.cacheMu.RLock()
	defer t.cacheMu.RUnlock()
	return t.currentExtractorURL, t.extractorExpiresAt
}

// SetCachedExtractorURL manually sets the cached extractor URL and expiration time.
func (t *ExtractorTier) SetCachedExtractorURL(url string, expiresAt time.Time) {
	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()
	clean := strings.TrimRight(strings.TrimSpace(url), "/")
	t.currentExtractorURL = clean
	t.extractorURL = clean
	t.extractorExpiresAt = expiresAt
}

// SetWorkerCacheConfig configures safety buffer and default cache durations (useful for testing).
func (t *ExtractorTier) SetWorkerCacheConfig(refreshBefore, cacheDuration time.Duration) {
	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()
	t.workerRefreshBefore = refreshBefore
	t.workerCacheDuration = cacheDuration
}

// isCacheValidLocked checks if the cached extractor URL is valid and not close to expiry.
func (t *ExtractorTier) isCacheValidLocked() bool {
	if t.currentExtractorURL == "" {
		return false
	}
	if t.extractorExpiresAt.IsZero() {
		return false
	}
	// If current time is close to expiry (within workerRefreshBefore), it is considered invalid.
	if time.Now().Add(t.workerRefreshBefore).After(t.extractorExpiresAt) {
		return false
	}
	return true
}

// getExtractorURL retrieves the current Pinggy extractor base URL, either from cache or from Worker.
// If forceRefresh is true and failedURL matches currentExtractorURL, the cache is bypassed.
func (t *ExtractorTier) getExtractorURL(ctx context.Context, forceRefresh bool, failedURL string) (string, error) {
	if !forceRefresh {
		t.cacheMu.RLock()
		if t.isCacheValidLocked() {
			cached := t.currentExtractorURL
			t.cacheMu.RUnlock()
			return cached, nil
		}
		t.cacheMu.RUnlock()
	}

	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()

	// Double check after acquiring write lock
	if !forceRefresh && t.isCacheValidLocked() {
		return t.currentExtractorURL, nil
	}

	// If force-refresh was requested because failedURL failed, check if another goroutine
	// already refreshed to a different, valid URL while we were waiting for the lock
	if forceRefresh && failedURL != "" && t.currentExtractorURL != "" && t.currentExtractorURL != failedURL && t.isCacheValidLocked() {
		log.Printf("[tier2] extractor URL already refreshed by another concurrent request to: %s", t.currentExtractorURL)
		return t.currentExtractorURL, nil
	}

	return t.fetchWorkerURLLocked(ctx)
}

// fetchWorkerURLLocked contacts the Worker API to obtain the current Pinggy URL. Must be called under cacheMu lock.
func (t *ExtractorTier) fetchWorkerURLLocked(ctx context.Context) (string, error) {
	if t.workerURL == "" {
		return "", fmt.Errorf("worker URL is not configured")
	}

	log.Printf("[tier2] requesting Pinggy extractor URL from worker: %s", t.workerURL)

	req, err := http.NewRequestWithContext(ctx, "GET", t.workerURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create worker request: %w", err)
	}
	req.Header.Set("User-Agent", extractorUserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Pinggy-No-Screen", "true")

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("network error calling worker: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("worker returned HTTP status %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed reading worker response: %w", err)
	}

	var wResp workerURLResponse
	if err := json.Unmarshal(bodyBytes, &wResp); err != nil {
		return "", fmt.Errorf("failed parsing worker json response: %w", err)
	}

	cleanURL := strings.TrimRight(strings.TrimSpace(wResp.URL), "/")
	if cleanURL == "" {
		return "", fmt.Errorf("worker returned missing or empty 'url'")
	}

	var expiresAt time.Time
	if wResp.ExpiresAt > 0 {
		expiresAt = time.Unix(wResp.ExpiresAt, 0)
	} else {
		expiresAt = time.Now().Add(t.workerCacheDuration)
	}

	t.currentExtractorURL = cleanURL
	t.extractorURL = cleanURL
	t.extractorExpiresAt = expiresAt

	log.Printf("[tier2] cached extractor URL: %s (expires: %s)", cleanURL, expiresAt.Format(time.RFC3339))
	return cleanURL, nil
}

// extractorAPIResponse models possible extractor response formats.
type extractorAPIResponse struct {
	Success *bool   `json:"success,omitempty"`
	Status  string  `json:"status,omitempty"`
	URL     *string `json:"url,omitempty"`
	Error   string  `json:"error,omitempty"`
	Message string  `json:"message,omitempty"`
}

// ResolveMovie resolves a movie via Tier 2 VidFast extractor.
func (t *ExtractorTier) ResolveMovie(ctx context.Context, tmdbID int) (*models.ResolutionResult, error) {
	targetURL := fmt.Sprintf("%s/movie/%d", t.vidfastBaseURL, tmdbID)
	log.Printf("[tier2] extracting VidFast URL for movie %d: %s", tmdbID, targetURL)

	streamURL, err := t.callExtractor(ctx, targetURL)
	if err != nil {
		log.Printf("[tier2] failed: %v", err)
		return nil, nil // Return nil to allow fallback to Tier 3
	}

	if streamURL == "" {
		log.Printf("[tier2] failed: extractor returned empty url")
		return nil, nil
	}

	log.Printf("[tier2] success resolving movie %d", tmdbID)
	return &models.ResolutionResult{
		Success: true,
		TmdbID:  tmdbID,
		Type:    "movie",
		Tier:    2,
		Source:  "vidfast-extractor",
		URL:     streamURL,
		Headers: map[string]string{
			"Referer": t.vidfastBaseURL + "/",
			"Origin":  t.vidfastBaseURL,
		},
	}, nil
}

// ResolveTV resolves a TV episode via Tier 2 VidFast extractor.
func (t *ExtractorTier) ResolveTV(ctx context.Context, tmdbID, season, episode int) (*models.ResolutionResult, error) {
	targetURL := fmt.Sprintf("%s/tv/%d/%d/%d", t.vidfastBaseURL, tmdbID, season, episode)
	log.Printf("[tier2] extracting VidFast URL for TV %d S%dE%d: %s", tmdbID, season, episode, targetURL)

	streamURL, err := t.callExtractor(ctx, targetURL)
	if err != nil {
		log.Printf("[tier2] failed: %v", err)
		return nil, nil // Return nil to allow fallback to Tier 3
	}

	if streamURL == "" {
		log.Printf("[tier2] failed: extractor returned empty url")
		return nil, nil
	}

	log.Printf("[tier2] success resolving TV %d S%dE%d", tmdbID, season, episode)
	return &models.ResolutionResult{
		Success: true,
		TmdbID:  tmdbID,
		Type:    "tv",
		Tier:    2,
		Source:  "vidfast-extractor",
		Season:  &season,
		Episode: &episode,
		URL:     streamURL,
		Headers: map[string]string{
			"Referer": t.vidfastBaseURL + "/",
			"Origin":  t.vidfastBaseURL,
		},
	}, nil
}

// callExtractor contacts the extractor service with proper URL encoding, automatic discovery,
// and single-retry on network failure.
func (t *ExtractorTier) callExtractor(ctx context.Context, targetURL string) (string, error) {
	baseURL, err := t.getExtractorURL(ctx, false, "")
	if err != nil {
		return "", fmt.Errorf("failed obtaining extractor base URL: %w", err)
	}

	streamURL, isNetFail, err := t.doExtractRequest(ctx, baseURL, targetURL)
	if err == nil {
		return streamURL, nil
	}

	// Single retry on network failure
	if isNetFail {
		log.Printf("[tier2] extractor request to %s failed with network error (%v); force-refreshing worker URL for retry", baseURL, err)
		refreshedBaseURL, refErr := t.getExtractorURL(ctx, true, baseURL)
		if refErr != nil {
			return "", fmt.Errorf("extractor request failed (%v) and worker refresh failed: %w", err, refErr)
		}

		log.Printf("[tier2] retrying extractor request once with refreshed URL: %s", refreshedBaseURL)
		retryStreamURL, _, retryErr := t.doExtractRequest(ctx, refreshedBaseURL, targetURL)
		if retryErr != nil {
			return "", fmt.Errorf("extractor retry failed: %w", retryErr)
		}
		return retryStreamURL, nil
	}

	return "", err
}

// doExtractRequest performs a single HTTP request to the extractor service.
func (t *ExtractorTier) doExtractRequest(ctx context.Context, baseURL, targetURL string) (string, bool, error) {
	cleanBase := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsedExtractor, err := url.Parse(cleanBase + "/extract")
	if err != nil {
		return "", false, fmt.Errorf("invalid extractor base url: %w", err)
	}

	params := url.Values{}
	params.Set("url", targetURL)
	timeoutSec := t.timeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = defaultTimeoutSeconds
	}
	params.Set("timeout", strconv.Itoa(timeoutSec))
	parsedExtractor.RawQuery = params.Encode()
	fullURL := parsedExtractor.String()

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return "", false, fmt.Errorf("failed to create extractor request: %w", err)
	}
	req.Header.Set("User-Agent", extractorUserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Pinggy-No-Screen", "true")

	resp, err := t.client.Do(req)
	if err != nil {
		return "", true, fmt.Errorf("network error calling extractor: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		isNetFail := resp.StatusCode >= 500 || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadGateway
		return "", isNetFail, fmt.Errorf("extractor http status %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", true, fmt.Errorf("failed reading extractor response: %w", err)
	}

	var apiResp extractorAPIResponse
	if err := json.Unmarshal(bodyBytes, &apiResp); err != nil {
		isNetFail := strings.HasPrefix(strings.TrimSpace(string(bodyBytes)), "<")
		return "", isNetFail, fmt.Errorf("failed parsing extractor json response: %w", err)
	}

	isSuccess := false
	if apiResp.Success != nil && *apiResp.Success {
		isSuccess = true
	} else if strings.EqualFold(apiResp.Status, "success") {
		isSuccess = true
	}

	if !isSuccess {
		errMsg := apiResp.Error
		if errMsg == "" {
			errMsg = apiResp.Message
		}
		if errMsg == "" {
			errMsg = "extractor returned success=false"
		}
		return "", false, fmt.Errorf("%s", errMsg)
	}

	if apiResp.URL == nil || strings.TrimSpace(*apiResp.URL) == "" {
		return "", false, fmt.Errorf("extractor returned null or empty url")
	}

	return strings.TrimSpace(*apiResp.URL), false, nil
}
