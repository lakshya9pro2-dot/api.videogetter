package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vidara-api/models"
	"vidara-api/proxy"
	"vidara-api/resolver"
)

func TestAPI_LiveWorker_TurboOctoSuccotash(t *testing.T) {
	liveWorkerURL := "https://turbo-octo-succotash.kineflex-netflex.workers.dev"

	mockTier3 := &mockTier{
		movieFn: func(ctx context.Context, id int) (*models.ResolutionResult, error) {
			return &models.ResolutionResult{
				Success: true,
				TmdbID:  id,
				Type:    "movie",
				Tier:    3,
				Source:  "live-test-source",
				Title:   "Big Buck Bunny Live Test",
				URL:     "https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8",
				Headers: map[string]string{
					"Origin":  "https://upstream.example.com",
					"Referer": "https://upstream.example.com/embed",
				},
			}, nil
		},
	}

	r := resolver.NewCentralResolver(nil, nil, mockTier3)
	h := NewAPIHandler(r, nil, nil, 10*time.Second)
	wc := proxy.NewWorkerClient([]string{liveWorkerURL}, "", 10*time.Second)
	h.SetWorkerClient(wc, false)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/movie/", h.HandleMovie)
	server := httptest.NewServer(mux)
	defer server.Close()

	// Call Go API with ?web
	resp, err := http.Get(server.URL + "/api/movie/550?web")
	if err != nil {
		t.Fatalf("API request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from API, got %d", resp.StatusCode)
	}

	var res models.ResolutionResult
	_ = json.NewDecoder(resp.Body).Decode(&res)

	if !res.Success {
		t.Fatalf("expected success=true, got: %+v", res)
	}

	// Verify URL was transformed to the live Cloudflare Worker URL
	if !strings.HasPrefix(res.URL, liveWorkerURL+"/session/") {
		t.Errorf("expected URL to point to live worker %s, got %s", liveWorkerURL, res.URL)
	}
	if !strings.HasSuffix(res.URL, "/master.m3u8") {
		t.Errorf("expected URL to end with /master.m3u8, got %s", res.URL)
	}

	// Verify upstream Origin and Referer are NOT leaked to the client
	if res.Headers["Origin"] != "" || res.Headers["Referer"] != "" {
		t.Errorf("upstream Origin/Referer should be stripped from client response, got: %+v", res.Headers)
	}

	// Verify media metadata preserved
	if res.TmdbID != 550 || res.Title != "Big Buck Bunny Live Test" || res.Tier != 3 {
		t.Errorf("metadata corrupted: %+v", res)
	}

	// Normal call without ?web must return direct URL without proxy
	respNorm, err := http.Get(server.URL + "/api/movie/550")
	if err != nil {
		t.Fatalf("API normal request failed: %v", err)
	}
	defer respNorm.Body.Close()

	var resNorm models.ResolutionResult
	_ = json.NewDecoder(respNorm.Body).Decode(&resNorm)
	if resNorm.URL != "https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8" {
		t.Errorf("expected normal request without ?web to return direct URL, got %s", resNorm.URL)
	}
	if resNorm.Headers["Origin"] != "https://upstream.example.com" {
		t.Errorf("expected normal request to preserve Origin header, got %+v", resNorm.Headers)
	}
}
