package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerClient_Worker1Succeeds(t *testing.T) {
	var w1Calls, w2Calls, w3Calls int32

	s1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&w1Calls, 1)
		if r.Header.Get("X-Worker-Key") != "secret123" {
			t.Errorf("expected secret123 in X-Worker-Key, got %s", r.Header.Get("X-Worker-Key"))
		}
		var req SessionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(SessionResponse{
			Success:     true,
			SessionID:   "sess-w1",
			PlaybackURL: "http://" + r.Host + "/session/sess-w1/master.m3u8",
		})
	}))
	defer s1.Close()

	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&w2Calls, 1)
	}))
	defer s2.Close()

	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&w3Calls, 1)
	}))
	defer s3.Close()

	client := NewWorkerClient([]string{s1.URL, s2.URL, s3.URL}, "secret123", 2*time.Second)

	res, err := client.CreateSession(context.Background(), SessionRequest{
		URL:     "https://upstream.example.com/master.m3u8",
		Headers: map[string]string{"Origin": "https://upstream.example.com"},
		VideoID: "550",
		Type:    "movie",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.SessionID != "sess-w1" {
		t.Errorf("expected sess-w1, got %s", res.SessionID)
	}
	if !strings.Contains(res.PlaybackURL, "/session/sess-w1/master.m3u8") {
		t.Errorf("unexpected playback url: %s", res.PlaybackURL)
	}
	if atomic.LoadInt32(&w1Calls) != 1 {
		t.Errorf("expected 1 call to Worker 1, got %d", w1Calls)
	}
	if atomic.LoadInt32(&w2Calls) != 0 || atomic.LoadInt32(&w3Calls) != 0 {
		t.Errorf("expected 0 calls to Worker 2 and 3, got w2=%d, w3=%d", w2Calls, w3Calls)
	}
}

func TestWorkerClient_Worker1Fails_Worker2Succeeds(t *testing.T) {
	var w1Calls, w2Calls, w3Calls int32

	// Worker 1 returns HTTP 500 error
	s1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&w1Calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "worker_internal_error"})
	}))
	defer s1.Close()

	// Worker 2 succeeds
	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&w2Calls, 1)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(SessionResponse{
			Success:     true,
			SessionID:   "sess-w2",
			PlaybackURL: "http://" + r.Host + "/session/sess-w2/master.m3u8",
		})
	}))
	defer s2.Close()

	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&w3Calls, 1)
	}))
	defer s3.Close()

	client := NewWorkerClient([]string{s1.URL, s2.URL, s3.URL}, "", 2*time.Second)

	res, err := client.CreateSession(context.Background(), SessionRequest{
		URL: "https://upstream.example.com/stream.m3u8",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.SessionID != "sess-w2" {
		t.Errorf("expected sess-w2, got %s", res.SessionID)
	}
	if atomic.LoadInt32(&w1Calls) != 1 || atomic.LoadInt32(&w2Calls) != 1 {
		t.Errorf("expected 1 call each to w1 and w2, got w1=%d, w2=%d", w1Calls, w2Calls)
	}
	if atomic.LoadInt32(&w3Calls) != 0 {
		t.Errorf("expected 0 calls to Worker 3, got %d", w3Calls)
	}
}

func TestWorkerClient_Worker1And2Fail_Worker3Succeeds(t *testing.T) {
	var w1Calls, w2Calls, w3Calls int32

	// Worker 1 returns invalid JSON
	s1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&w1Calls, 1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	}))
	defer s1.Close()

	// Worker 2 returns success: false
	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&w2Calls, 1)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(SessionResponse{
			Success: false,
			Error:   "upstream_rate_limited",
		})
	}))
	defer s2.Close()

	// Worker 3 succeeds
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&w3Calls, 1)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(SessionResponse{
			Success:     true,
			SessionID:   "sess-w3",
			PlaybackURL: "http://" + r.Host + "/session/sess-w3/master.m3u8",
		})
	}))
	defer s3.Close()

	client := NewWorkerClient([]string{s1.URL, s2.URL, s3.URL}, "", 2*time.Second)

	res, err := client.CreateSession(context.Background(), SessionRequest{
		URL: "https://upstream.example.com/stream.m3u8",
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.SessionID != "sess-w3" {
		t.Errorf("expected sess-w3, got %s", res.SessionID)
	}
	if atomic.LoadInt32(&w1Calls) != 1 || atomic.LoadInt32(&w2Calls) != 1 || atomic.LoadInt32(&w3Calls) != 1 {
		t.Errorf("expected 1 call each to w1, w2, and w3; got w1=%d, w2=%d, w3=%d", w1Calls, w2Calls, w3Calls)
	}
}

func TestWorkerClient_AllWorkersFail_StructuredError(t *testing.T) {
	s1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer s1.Close()

	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer s2.Close()

	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer s3.Close()

	client := NewWorkerClient([]string{s1.URL, s2.URL, s3.URL}, "", 2*time.Second)

	_, err := client.CreateSession(context.Background(), SessionRequest{
		URL: "https://upstream.example.com/stream.m3u8",
	})

	if err == nil {
		t.Fatal("expected error when all workers fail, got nil")
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "all configured proxy workers failed") {
		t.Errorf("expected 'all configured proxy workers failed', got: %s", errStr)
	}
	if !strings.Contains(errStr, "HTTP 502") || !strings.Contains(errStr, "HTTP 503") || !strings.Contains(errStr, "HTTP 504") {
		t.Errorf("expected failure reasons in error string, got: %s", errStr)
	}
}

func TestWorkerClient_HostMismatchRejected(t *testing.T) {
	// Worker returns playback URL for rogue external domain
	s1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(SessionResponse{
			Success:     true,
			SessionID:   "sess-rogue",
			PlaybackURL: "https://rogue-malicious-domain.com/evil/master.m3u8",
		})
	}))
	defer s1.Close()

	client := NewWorkerClient([]string{s1.URL}, "", 2*time.Second)

	_, err := client.CreateSession(context.Background(), SessionRequest{
		URL: "https://upstream.example.com/stream.m3u8",
	})

	if err == nil {
		t.Fatal("expected error due to mismatched hostname in playback URL, got nil")
	}
	if !strings.Contains(err.Error(), "mismatched hostname") {
		t.Errorf("expected mismatched hostname error, got: %v", err)
	}
}

func TestWorkerClient_TimeoutHandling(t *testing.T) {
	// Worker hangs longer than client timeout
	s1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer s1.Close()

	// 50ms timeout
	client := NewWorkerClient([]string{s1.URL}, "", 50*time.Millisecond)

	_, err := client.CreateSession(context.Background(), SessionRequest{
		URL: "https://upstream.example.com/stream.m3u8",
	})

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "all configured proxy workers failed") {
		t.Errorf("expected failover exhaustion error, got: %v", err)
	}
}

func TestWorkerClient_ExtractSession_Success(t *testing.T) {
	s1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/extract" {
			t.Errorf("expected /extract path, got %s", r.URL.Path)
		}
		var req ExtractSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed decoding request: %v", err)
		}
		if req.Provider != "streamhg" || req.ID != "testcode123" {
			t.Errorf("unexpected payload: %+v", req)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(SessionResponse{
			Success:     true,
			SessionID:   "sess-extract-1",
			PlaybackURL: "http://" + r.Host + "/session/sess-extract-1/master.m3u8",
		})
	}))
	defer s1.Close()

	client := NewWorkerClient([]string{s1.URL}, "", 2*time.Second)
	res, err := client.ExtractSession(context.Background(), ExtractSessionRequest{
		Provider: "streamhg",
		ID:       "testcode123",
		VideoID:  "550",
		Type:     "movie",
	})
	if err != nil {
		t.Fatalf("ExtractSession failed: %v", err)
	}
	if res.SessionID != "sess-extract-1" {
		t.Errorf("expected sess-extract-1, got %s", res.SessionID)
	}
	if !strings.Contains(res.PlaybackURL, "/session/sess-extract-1/master.m3u8") {
		t.Errorf("unexpected playback url: %s", res.PlaybackURL)
	}
}
