package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SessionRequest is the payload sent to Cloudflare Workers to create a playback session.
type SessionRequest struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	VideoID string            `json:"videoId,omitempty"`
	Type    string            `json:"type,omitempty"`
}

// SessionResponse is the response payload returned by Cloudflare Workers.
type SessionResponse struct {
	Success     bool   `json:"success"`
	SessionID   string `json:"session_id,omitempty"`
	PlaybackURL string `json:"playback_url,omitempty"`
	URL         string `json:"url,omitempty"`
	VideoID     string `json:"video_id,omitempty"`
	Type        string `json:"type,omitempty"`
	ExpiresAt   int64  `json:"expires_at,omitempty"`
	Error       string `json:"error,omitempty"`
	Message     string `json:"message,omitempty"`
}

// WorkerClient manages communication and sequential failover across configured Cloudflare Workers.
//
// CRITICAL ARCHITECTURE RULE:
// This component is strictly an HTTP API client for Worker session registration.
// It DOES NOT download, rewrite, parse, or relay any HLS playlists or media segments.
type WorkerClient struct {
	workerURLs []string
	secret     string
	timeout    time.Duration
	httpClient *http.Client
}

// NewWorkerClient creates a new WorkerClient.
func NewWorkerClient(workerURLs []string, secret string, timeout time.Duration) *WorkerClient {
	var cleanURLs []string
	for _, u := range workerURLs {
		trimmed := strings.TrimRight(strings.TrimSpace(u), "/")
		if trimmed != "" {
			cleanURLs = append(cleanURLs, trimmed)
		}
	}

	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	return &WorkerClient{
		workerURLs: cleanURLs,
		secret:     strings.TrimSpace(secret),
		timeout:    timeout,
		httpClient: &http.Client{
			Timeout: timeout + 2*time.Second,
		},
	}
}

// HasWorkers returns true if at least one Worker URL is configured.
func (c *WorkerClient) HasWorkers() bool {
	return len(c.workerURLs) > 0
}

// GetWorkerURLs returns a copy of configured worker URLs.
func (c *WorkerClient) GetWorkerURLs() []string {
	res := make([]string, len(c.workerURLs))
	copy(res, c.workerURLs)
	return res
}

// CreateSession attempts to create a playback session by contacting configured Workers in order.
// If Worker 1 fails, it automatically fails over to Worker 2, then Worker 3.
func (c *WorkerClient) CreateSession(ctx context.Context, req SessionRequest) (*SessionResponse, error) {
	if !c.HasWorkers() {
		return nil, fmt.Errorf("no proxy workers configured")
	}

	cleanTargetURL := strings.TrimSpace(req.URL)
	if cleanTargetURL == "" {
		return nil, fmt.Errorf("upstream url is empty")
	}

	reqBodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize session request: %w", err)
	}

	var failures []string

	for idx, workerBase := range c.workerURLs {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		attemptCtx, cancel := context.WithTimeout(ctx, c.timeout)

		endpoint := workerBase + "/session"
		httpReq, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, endpoint, bytes.NewReader(reqBodyBytes))
		if err != nil {
			cancel()
			failures = append(failures, fmt.Sprintf("worker-%d (%s): init request failed: %v", idx+1, workerBase, err))
			continue
		}

		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "application/json")
		httpReq.Header.Set("User-Agent", "Vidara-Backend-WorkerClient/1.0")

		if c.secret != "" {
			httpReq.Header.Set("X-Worker-Key", c.secret)
		}

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			cancel()
			log.Printf("[worker-proxy] worker-%d (%s) attempt failed: %v", idx+1, workerBase, err)
			failures = append(failures, fmt.Sprintf("worker-%d (%s): connection error: %v", idx+1, workerBase, err))
			continue
		}

		bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		_ = resp.Body.Close()
		cancel()

		if readErr != nil {
			log.Printf("[worker-proxy] worker-%d (%s) failed reading response: %v", idx+1, workerBase, readErr)
			failures = append(failures, fmt.Sprintf("worker-%d (%s): read error: %v", idx+1, workerBase, readErr))
			continue
		}

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			log.Printf("[worker-proxy] worker-%d (%s) returned HTTP %d: %s", idx+1, workerBase, resp.StatusCode, string(bodyBytes))
			failures = append(failures, fmt.Sprintf("worker-%d (%s): HTTP %d", idx+1, workerBase, resp.StatusCode))
			continue
		}

		var sessResp SessionResponse
		if err := json.Unmarshal(bodyBytes, &sessResp); err != nil {
			log.Printf("[worker-proxy] worker-%d (%s) returned invalid JSON: %v", idx+1, workerBase, err)
			failures = append(failures, fmt.Sprintf("worker-%d (%s): invalid json: %v", idx+1, workerBase, err))
			continue
		}

		if !sessResp.Success {
			errMsg := sessResp.Message
			if errMsg == "" {
				errMsg = sessResp.Error
			}
			log.Printf("[worker-proxy] worker-%d (%s) returned unsuccessful status: %s", idx+1, workerBase, errMsg)
			failures = append(failures, fmt.Sprintf("worker-%d (%s): worker error: %s", idx+1, workerBase, errMsg))
			continue
		}

		playbackURL := strings.TrimSpace(sessResp.PlaybackURL)
		if playbackURL == "" {
			playbackURL = strings.TrimSpace(sessResp.URL)
		}

		if playbackURL == "" {
			log.Printf("[worker-proxy] worker-%d (%s) returned empty playback URL", idx+1, workerBase)
			failures = append(failures, fmt.Sprintf("worker-%d (%s): empty playback URL", idx+1, workerBase))
			continue
		}

		// Security: Validate playback URL protocol and host
		parsedPlayback, err := url.Parse(playbackURL)
		if err != nil {
			log.Printf("[worker-proxy] worker-%d (%s) returned unparseable playback URL: %v", idx+1, workerBase, err)
			failures = append(failures, fmt.Sprintf("worker-%d (%s): unparseable playback URL: %v", idx+1, workerBase, err))
			continue
		}

		parsedWorker, err := url.Parse(workerBase)
		if err != nil {
			parsedWorker = &url.URL{}
		}

		// Must use HTTPS (or HTTP if testing on localhost/127.0.0.1)
		isLocal := parsedPlayback.Hostname() == "127.0.0.1" || parsedPlayback.Hostname() == "localhost"
		if parsedPlayback.Scheme != "https" && !isLocal {
			log.Printf("[worker-proxy] worker-%d (%s) returned non-HTTPS URL: %s", idx+1, workerBase, playbackURL)
			failures = append(failures, fmt.Sprintf("worker-%d (%s): non-https playback URL", idx+1, workerBase))
			continue
		}

		// Hostname must match the responding worker
		if !strings.EqualFold(parsedPlayback.Hostname(), parsedWorker.Hostname()) {
			log.Printf("[worker-proxy] worker-%d (%s) returned mismatched hostname: %s vs %s",
				idx+1, workerBase, parsedPlayback.Hostname(), parsedWorker.Hostname())
			failures = append(failures, fmt.Sprintf("worker-%d (%s): mismatched hostname in playback URL", idx+1, workerBase))
			continue
		}

		log.Printf("[worker-proxy] successfully created session via worker-%d (%s)", idx+1, workerBase)
		sessResp.PlaybackURL = playbackURL
		sessResp.URL = playbackURL
		return &sessResp, nil
	}

	return nil, fmt.Errorf("all configured proxy workers failed: %s", strings.Join(failures, "; "))
}
