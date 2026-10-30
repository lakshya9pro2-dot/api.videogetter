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
	"regexp"
	"strings"
	"sync"
	"time"

	"vidara-api/models"
)

const (
	// DefaultVidriftBaseURL is the base URL for the Vidrift embed host.
	DefaultVidriftBaseURL = "https://embed.vidrift.in"

	// DefaultCineTVBaseURL is the base URL for the CineTV stream worker (Tier 3).
	DefaultCineTVBaseURL = "https://cinetvtest.netlify.app"

	// DefaultVidriftUserAgent is the browser UA used for requests.
	DefaultVidriftUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

// CineTVResponse represents the JSON response from CineTV resolver (cinetvtest.netlify.app).
type CineTVResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Season  string `json:"season,omitempty"`
	Episode string `json:"episode,omitempty"`
	URL     string `json:"url"`
	Error   string `json:"error,omitempty"`
}

var (
	// reEmbedMeta extracts the embedMeta JSON object embedded in Vidrift HTML shells.
	reEmbedMeta = regexp.MustCompile(`(?s)embedMeta\s*=\s*(\{.*?\});\s*(?:</script>|var\s+|const\s+|let\s+|\n)`)
)

// VidriftStreamItem models individual stream objects returned by Vidrift embed or API.
type VidriftStreamItem struct {
	Index    int    `json:"index"`
	URL      string `json:"url"`
	ProxyURL string `json:"proxyUrl"`
	Type     string `json:"type"`
	Provider string `json:"provider"`
	Direct   bool   `json:"direct"`
	Name     string `json:"name,omitempty"`
}

// VidriftEmbedMeta models the embedMeta JSON object embedded in Vidrift HTML shells.
type VidriftEmbedMeta struct {
	TmdbID        interface{}         `json:"tmdbId"`
	Type          string              `json:"type"`
	Season        interface{}         `json:"season"`
	Episode       interface{}         `json:"episode"`
	PlaybackToken string              `json:"playbackToken"`
	EvionURL      string              `json:"evionUrl"`
	VexonURL      string              `json:"vexonUrl"`
	OrionStreams  []VidriftStreamItem `json:"orionStreams"`
}

// VidriftAPIResponse models the JSON returned by /api/source/...
type VidriftAPIResponse struct {
	Success  bool                `json:"success"`
	TmdbID   interface{}         `json:"tmdbId"`
	Type     string              `json:"type"`
	Provider string              `json:"provider"`
	Streams  []VidriftStreamItem `json:"streams"`
}

// DefaultVidriftAPIProviders contains the ordered fallback list of API providers.
// ============================================================================
// EASY EXTENSION:
// To add a new API provider (e.g., "nova", "streamhub"), simply append it to
// this slice, or call tier3.AddAPIProvider("nova") at runtime!
// ============================================================================
var DefaultVidriftAPIProviders = []string{
	"atlas", // Priority 2 (1st API Fallback): Standard MPEG-TS HLS (1080p - 360p)
	"vela",  // Priority 3 (2nd API Fallback): High-quality fMP4 HLS
}

// VidriftTier represents Tier 3 resolving via CineTV and Vidrift (CineTV -> Evion -> Atlas -> Vela -> Orion).
type VidriftTier struct {
	mu            sync.RWMutex
	baseURL       string
	cinetvBaseURL string
	tmdbKey       string
	apiProviders  []string
	client        *http.Client
}

// NewVidriftTier creates a new Tier 3 resolver.
func NewVidriftTier(baseURL, tmdbKey string, timeout time.Duration) *VidriftTier {
	if baseURL == "" {
		baseURL = DefaultVidriftBaseURL
	}
	if tmdbKey == "" {
		tmdbKey = "e6333b32409e02a4a6eba6fb7ff866bb"
	}

	providers := make([]string, len(DefaultVidriftAPIProviders))
	copy(providers, DefaultVidriftAPIProviders)

	cinetvURL := os.Getenv("CINETV_BASE_URL")

	return &VidriftTier{
		baseURL:       strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		cinetvBaseURL: strings.TrimRight(strings.TrimSpace(cinetvURL), "/"),
		tmdbKey:       tmdbKey,
		apiProviders:  providers,
		client:        &http.Client{Timeout: timeout},
	}
}

// SetAPIProviders overrides the fallback provider list with custom providers.
func (v *VidriftTier) SetAPIProviders(providers ...string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.apiProviders = append([]string{}, providers...)
}

// AddAPIProvider appends an additional provider to the fallback list.
func (v *VidriftTier) AddAPIProvider(provider string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	provider = strings.TrimSpace(strings.ToLower(provider))
	if provider != "" {
		v.apiProviders = append(v.apiProviders, provider)
	}
}

// GetAPIProviders returns a copy of current configured API fallback providers.
func (v *VidriftTier) GetAPIProviders() []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	res := make([]string, len(v.apiProviders))
	copy(res, v.apiProviders)
	return res
}

// SetCineTVBaseURL sets the base URL for CineTV resolver.
func (v *VidriftTier) SetCineTVBaseURL(url string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.cinetvBaseURL = strings.TrimRight(strings.TrimSpace(url), "/")
}

// GetCineTVBaseURL gets the base URL for CineTV resolver.
func (v *VidriftTier) GetCineTVBaseURL() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.cinetvBaseURL
}

func (v *VidriftTier) resolveCineTVMovie(ctx context.Context, tmdbID int) (*models.ResolutionResult, error) {
	base := v.GetCineTVBaseURL()
	if base == "" {
		return nil, fmt.Errorf("cinetv base URL not configured")
	}

	reqURL := fmt.Sprintf("%s/api/movie/%d", base, tmdbID)
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", DefaultVidriftUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cinetv returned status %d", resp.StatusCode)
	}

	var data CineTVResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	if data.URL == "" || data.Error != "" {
		return nil, fmt.Errorf("cinetv returned no playable stream (error: %s)", data.Error)
	}

	title := data.Name
	if title == "" {
		title = v.FetchTitle(ctx, tmdbID, "movie")
	}
	if title == "" {
		title = fmt.Sprintf("Movie %d", tmdbID)
	}

	streamURL := data.URL
	if strings.HasPrefix(streamURL, "http://filmin.a0p3.com") {
		streamURL = "https://" + strings.TrimPrefix(streamURL, "http://")
	}

	log.Printf("[tier3] successfully resolved movie %d (%s) via cinetv: %s", tmdbID, title, streamURL)

	return &models.ResolutionResult{
		Success:    true,
		TmdbID:     tmdbID,
		Type:       "movie",
		Tier:       3,
		Source:     "cinetv",
		Title:      title,
		Name:       title,
		URL:        streamURL,
		Direct:     true,
		StreamType: "mp4",
		Headers:    map[string]string{},
		Play: &models.Playback{
			URL:  streamURL,
			Play: streamURL,
		},
	}, nil
}

func (v *VidriftTier) resolveCineTVTV(ctx context.Context, tmdbID, season, episode int) (*models.ResolutionResult, error) {
	base := v.GetCineTVBaseURL()
	if base == "" {
		return nil, fmt.Errorf("cinetv base URL not configured")
	}

	reqURL := fmt.Sprintf("%s/api/tv/%d/%d/%d", base, tmdbID, season, episode)
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", DefaultVidriftUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cinetv returned status %d", resp.StatusCode)
	}

	var data CineTVResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	if data.URL == "" || data.Error != "" {
		return nil, fmt.Errorf("cinetv returned no playable stream (error: %s)", data.Error)
	}

	title := data.Name
	if title == "" {
		title = v.FetchTitle(ctx, tmdbID, "tv")
	}
	if title == "" {
		title = fmt.Sprintf("TV %d", tmdbID)
	}

	streamURL := data.URL
	if strings.HasPrefix(streamURL, "http://filmin.a0p3.com") {
		streamURL = "https://" + strings.TrimPrefix(streamURL, "http://")
	}

	log.Printf("[tier3] successfully resolved TV %d S%dE%d (%s) via cinetv: %s", tmdbID, season, episode, title, streamURL)

	return &models.ResolutionResult{
		Success:    true,
		TmdbID:     tmdbID,
		Type:       "tv",
		Tier:       3,
		Source:     "cinetv",
		Title:      title,
		Name:       title,
		Season:     &season,
		Episode:    &episode,
		URL:        streamURL,
		Direct:     true,
		StreamType: "mp4",
		Headers:    map[string]string{},
		Play: &models.Playback{
			URL:  streamURL,
			Play: streamURL,
		},
	}, nil
}

// ResolveMovie resolves a movie TMDB ID via Tier 3 (CineTV -> Vidrift).
func (v *VidriftTier) ResolveMovie(ctx context.Context, tmdbID int) (*models.ResolutionResult, error) {
	log.Printf("[tier3] resolving movie %d via Tier 3", tmdbID)

	// Priority 0: CineTV (cinetvtest.netlify.app) - Direct MP4, no proxy needed
	if v.GetCineTVBaseURL() != "" {
		res, err := v.resolveCineTVMovie(ctx, tmdbID)
		if err == nil && res != nil && res.URL != "" {
			return res, nil
		}
		if err != nil {
			log.Printf("[tier3] CineTV movie %d failed: %v, falling back to Vidrift", tmdbID, err)
		}
	}

	mediaPath := fmt.Sprintf("movie/%d", tmdbID)

	streamURL, provider, err := v.ResolveStream(ctx, mediaPath)
	if err != nil || streamURL == "" {
		log.Printf("[tier3] movie %d error: %v", tmdbID, err)
		return nil, nil // Cascade cleanly to Tier 4
	}

	title := v.FetchTitle(ctx, tmdbID, "movie")
	if title == "" {
		title = fmt.Sprintf("Movie %d", tmdbID)
	}

	log.Printf("[tier3] successfully resolved movie %d (%s) via %s: %s", tmdbID, title, provider, streamURL)

	return &models.ResolutionResult{
		Success: true,
		TmdbID:  tmdbID,
		Type:    "movie",
		Tier:    3,
		Source:  "vidrift-" + provider,
		Title:   title,
		URL:     streamURL,
		Headers: map[string]string{
			"Origin":  v.baseURL,
			"Referer": v.baseURL + "/",
		},
	}, nil
}

// ResolveTV resolves a TV episode via Tier 3 (CineTV -> Vidrift).
func (v *VidriftTier) ResolveTV(ctx context.Context, tmdbID, season, episode int) (*models.ResolutionResult, error) {
	log.Printf("[tier3] resolving TV %d S%dE%d via Tier 3", tmdbID, season, episode)

	// Priority 0: CineTV (cinetvtest.netlify.app) - Direct MP4, no proxy needed
	if v.GetCineTVBaseURL() != "" {
		res, err := v.resolveCineTVTV(ctx, tmdbID, season, episode)
		if err == nil && res != nil && res.URL != "" {
			return res, nil
		}
		if err != nil {
			log.Printf("[tier3] CineTV TV %d S%dE%d failed: %v, falling back to Vidrift", tmdbID, season, episode, err)
		}
	}

	mediaPath := fmt.Sprintf("tv/%d/%d/%d", tmdbID, season, episode)

	streamURL, provider, err := v.ResolveStream(ctx, mediaPath)
	if err != nil || streamURL == "" {
		log.Printf("[tier3] TV %d S%dE%d error: %v", tmdbID, season, episode, err)
		return nil, nil // Cascade cleanly to Tier 4
	}

	title := v.FetchTitle(ctx, tmdbID, "tv")
	if title == "" {
		title = fmt.Sprintf("TV %d", tmdbID)
	}

	log.Printf("[tier3] successfully resolved TV %d S%dE%d (%s) via %s: %s", tmdbID, season, episode, title, provider, streamURL)

	return &models.ResolutionResult{
		Success: true,
		TmdbID:  tmdbID,
		Type:    "tv",
		Tier:    3,
		Source:  "vidrift-" + provider,
		Title:   title,
		Season:  &season,
		Episode: &episode,
		URL:     streamURL,
		Headers: map[string]string{
			"Origin":  v.baseURL,
			"Referer": v.baseURL + "/",
		},
	}, nil
}

// ResolveStream executes the multi-provider resolution pipeline:
// 1. Evion URL (Best / Recommended): Direct from embedMeta.evionUrl (zero additional API calls).
// 2. Atlas (1st Fallback): Queries /api/source with provider=atlas.
// 3. Vela (2nd Fallback): Queries /api/source with provider=vela.
// 4. Any custom API providers added to apiProviders.
// 5. Orion Streams: Direct master playlists embedded in embedMeta.orionStreams.
func (v *VidriftTier) ResolveStream(ctx context.Context, mediaPath string) (streamURL, provider string, err error) {
	embedURL := fmt.Sprintf("%s/embed/%s", v.baseURL, mediaPath)
	log.Printf("[tier3] fetching Vidrift embed metadata from %s", embedURL)

	meta, err := v.fetchEmbedMeta(ctx, embedURL)
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch embed metadata: %w", err)
	}

	// Priority 1: Evion URL (Best / Recommended)
	// Embedded directly in HTML (embedMeta.evionUrl). Zero additional API requests needed.
	// Master HLS with 1080p, multi-audio (9 languages), and subtitles.
	if evion := strings.TrimSpace(meta.EvionURL); evion != "" {
		log.Printf("[tier3] found direct evionUrl: %s", evion)
		return evion, "evion", nil
	}

	token := strings.TrimSpace(meta.PlaybackToken)
	if token != "" {
		// Priority 2 & 3: Iterate through configured API providers (default: atlas -> vela)
		providers := v.GetAPIProviders()
		for _, prov := range providers {
			log.Printf("[tier3] attempting API provider '%s' for %s", prov, mediaPath)
			url, err := v.queryAPIProvider(ctx, mediaPath, token, prov)
			if err == nil && url != "" {
				log.Printf("[tier3] successfully resolved via provider '%s'", prov)
				return url, prov, nil
			}
			log.Printf("[tier3] provider '%s' failed: %v", prov, err)
		}
	} else {
		log.Printf("[tier3] warning: embedMeta has no playbackToken")
	}

	// Priority 4: Orion Streams from embedMeta
	for _, os := range meta.OrionStreams {
		candidate := strings.TrimSpace(os.ProxyURL)
		if candidate == "" {
			candidate = strings.TrimSpace(os.URL)
		}
		if candidate != "" {
			if strings.HasPrefix(candidate, "/") {
				candidate = v.baseURL + candidate
			}
			log.Printf("[tier3] resolved via embedded Orion stream: %s", candidate)
			return candidate, "orion", nil
		}
	}

	return "", "", fmt.Errorf("all vidrift providers exhausted for %s", mediaPath)
}

// fetchEmbedMeta requests the HTML shell from Vidrift and extracts the embedMeta JSON object.
func (v *VidriftTier) fetchEmbedMeta(ctx context.Context, embedURL string) (*VidriftEmbedMeta, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", embedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", DefaultVidriftUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed page returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	m := reEmbedMeta.FindSubmatch(body)
	if len(m) < 2 {
		return nil, fmt.Errorf("embedMeta block not found in HTML response")
	}

	var meta VidriftEmbedMeta
	if err := json.Unmarshal(m[1], &meta); err != nil {
		return nil, fmt.Errorf("failed to parse embedMeta JSON: %w", err)
	}

	return &meta, nil
}

// queryAPIProvider calls /api/source/... for a specific provider (e.g. "atlas", "vela").
func (v *VidriftTier) queryAPIProvider(ctx context.Context, mediaPath, token, provider string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("empty playback token")
	}

	apiURL := fmt.Sprintf("%s/api/source/%s?token=%s&provider=%s&parent=",
		v.baseURL, mediaPath, url.QueryEscape(token), url.QueryEscape(provider))

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", DefaultVidriftUserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", fmt.Sprintf("%s/embed/%s", v.baseURL, mediaPath))

	resp, err := v.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("api provider '%s' returned HTTP %d", provider, resp.StatusCode)
	}

	var apiResp VidriftAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return "", fmt.Errorf("failed to decode '%s' JSON response: %w", provider, err)
	}

	if !apiResp.Success && len(apiResp.Streams) == 0 {
		return "", fmt.Errorf("provider '%s' returned success=false", provider)
	}

	for _, s := range apiResp.Streams {
		candidate := strings.TrimSpace(s.ProxyURL)
		if candidate == "" {
			candidate = strings.TrimSpace(s.URL)
		}
		if candidate != "" {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("no valid stream found for provider '%s'", provider)
}

// FetchTitle retrieves title metadata from TMDB with a bounded timeout.
func (v *VidriftTier) FetchTitle(ctx context.Context, tmdbID int, mediaType string) string {
	if v.tmdbKey == "" || tmdbID <= 0 {
		return ""
	}
	tmdbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	reqURL := fmt.Sprintf("https://api.themoviedb.org/3/%s/%d?api_key=%s", mediaType, tmdbID, v.tmdbKey)
	req, err := http.NewRequestWithContext(tmdbCtx, "GET", reqURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "vidara-api/1.0")

	resp, err := v.client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return ""
	}

	if t, ok := data["title"].(string); ok && t != "" {
		return t
	}
	if n, ok := data["name"].(string); ok && n != "" {
		return n
	}
	return ""
}
