package proxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLiveWorker_TurboOctoSuccotash(t *testing.T) {
	liveWorkerURL := "https://turbo-octo-succotash.kineflex-netflex.workers.dev"

	// 1. Verify health endpoint
	resp, err := http.Get(liveWorkerURL + "/health")
	if err != nil {
		t.Fatalf("failed to connect to live worker: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from live worker /health, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"service":"hls-playback-proxy-worker"`) {
		t.Errorf("unexpected health body: %s", string(body))
	}

	// 2. Create session using WorkerClient
	client := NewWorkerClient([]string{liveWorkerURL}, "", 15*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sess, err := client.CreateSession(ctx, SessionRequest{
		URL:     "https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8",
		VideoID: "mux-test-live",
		Type:    "movie",
	})

	if err != nil {
		t.Fatalf("CreateSession failed on live worker: %v", err)
	}

	if !sess.Success {
		t.Fatalf("expected success=true, got %+v", sess)
	}

	if !strings.HasPrefix(sess.PlaybackURL, liveWorkerURL+"/session/") {
		t.Fatalf("expected playback URL on %s, got %s", liveWorkerURL, sess.PlaybackURL)
	}

	// 3. Fetch rewritten master playlist from live worker playback URL
	playReq, err := http.NewRequestWithContext(ctx, "GET", sess.PlaybackURL, nil)
	if err != nil {
		t.Fatalf("failed to create play request: %v", err)
	}
	playResp, err := http.DefaultClient.Do(playReq)
	if err != nil {
		t.Fatalf("failed to fetch playback url from live worker: %v", err)
	}
	defer playResp.Body.Close()

	if playResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from playback URL, got %d", playResp.StatusCode)
	}

	contentType := playResp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "mpegurl") {
		t.Errorf("expected mpegurl Content-Type, got %s", contentType)
	}

	m3uBytes, _ := io.ReadAll(playResp.Body)
	m3uText := string(m3uBytes)

	if !strings.HasPrefix(m3uText, "#EXTM3U") {
		t.Errorf("expected playlist to start with #EXTM3U, got: %s", m3uText[:min(50, len(m3uText))])
	}

	if !strings.Contains(m3uText, liveWorkerURL+"/session/") {
		t.Errorf("expected playlist variants to be rewritten to live worker domain, got: %s", m3uText)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
