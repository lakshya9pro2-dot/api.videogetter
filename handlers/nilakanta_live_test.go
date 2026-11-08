package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vidara-api/models"
	"vidara-api/proxy"
	"vidara-api/resolver"
	"vidara-api/tiers"
)

func TestLive_Nilakanta_1107009_NetlifyProxy_NormalAndWeb(t *testing.T) {
	netlifyProxyURL := "https://kineflexvideoapi.netlify.app"

	// 1. Initialize Tier 1 with local dataset containing 1107009
	tier1 := tiers.NewVidaraTier("https://vidara.to", "https://streamtape.com", 25*time.Second)
	if err := tier1.LoadData("../data/vidara.json"); err != nil {
		t.Fatalf("failed to load dataset: %v", err)
	}

	workerClient := proxy.NewWorkerClient([]string{netlifyProxyURL}, "", 25*time.Second)
	tier1.SetWorkerClient(workerClient)

	centralResolver := resolver.NewCentralResolver(tier1, nil, nil)
	h := NewAPIHandler(centralResolver, tier1, nil, 25*time.Second)
	h.SetWorkerClient(workerClient, false)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/movie/", h.HandleMovie)
	server := httptest.NewServer(mux)
	defer server.Close()

	var playbackURL string

	// 2. Test Normal Request: GET /api/movie/1107009
	t.Run("NormalMode_ProxyStreamtape", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/api/movie/1107009")
		if err != nil {
			t.Fatalf("GET /api/movie/1107009 failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200 OK, got %d (body: %s)", resp.StatusCode, string(body))
		}

		var res models.ResolutionResult
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if !res.Success {
			t.Fatalf("expected success=true, got %+v", res)
		}
		if res.Source != "streamtape" {
			t.Errorf("expected source streamtape, got %s", res.Source)
		}
		if !strings.HasPrefix(res.URL, netlifyProxyURL+"/session/") {
			t.Errorf("expected URL to point to Netlify proxy %s, got %s", netlifyProxyURL, res.URL)
		}
		if !strings.HasSuffix(res.URL, "/video.mp4") {
			t.Errorf("expected URL to end with /video.mp4, got %s", res.URL)
		}

		playbackURL = res.URL
	})

	// 3. Test Web Request: GET /api/movie/1107009?web
	t.Run("WebMode_ProxyStreamtape", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/api/movie/1107009?web")
		if err != nil {
			t.Fatalf("GET /api/movie/1107009?web failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200 OK, got %d (body: %s)", resp.StatusCode, string(body))
		}

		var res models.ResolutionResult
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if !res.Success {
			t.Fatalf("expected success=true, got %+v", res)
		}
		if res.Source != "streamtape" {
			t.Errorf("expected source streamtape, got %s", res.Source)
		}
		if !strings.HasPrefix(res.URL, netlifyProxyURL+"/session/") {
			t.Errorf("expected URL to point to Netlify proxy %s, got %s", netlifyProxyURL, res.URL)
		}
	})

	// 4. Test Live 206 Partial Content Range Streaming from the Playback URL
	t.Run("LiveStreaming_Range206", func(t *testing.T) {
		if playbackURL == "" {
			t.Skip("playbackURL not set from normal test")
		}

		req, err := http.NewRequest("GET", playbackURL, nil)
		if err != nil {
			t.Fatalf("failed to create range request: %v", err)
		}
		req.Header.Set("Range", "bytes=0-100")

		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to execute range request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusPartialContent {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 206 Partial Content, got %d (body: %s)", resp.StatusCode, string(body))
		}

		cr := resp.Header.Get("Content-Range")
		if !strings.HasPrefix(cr, "bytes 0-100/") {
			t.Errorf("expected Content-Range starting with 'bytes 0-100/', got: %s", cr)
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed reading partial content body: %v", err)
		}
		if len(bodyBytes) != 101 {
			t.Errorf("expected 101 bytes, got %d", len(bodyBytes))
		}
	})
}
