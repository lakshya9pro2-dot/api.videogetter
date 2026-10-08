package tiers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 1. Verify Worker URL retrieval and JSON parsing
func TestExtractorTier_WorkerURLRetrieval_And_JSONParsing(t *testing.T) {
	var workerCalled int32
	var extractorCalled int32

	// Mock Pinggy Extractor Server
	extractorTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&extractorCalled, 1)
		if r.URL.Path != "/extract" {
			t.Errorf("expected path /extract, got %s", r.URL.Path)
		}
		target := r.URL.Query().Get("url")
		if target != "https://vidfast.vc/movie/126560" {
			t.Errorf("expected url 'https://vidfast.vc/movie/126560', got '%s'", target)
		}
		timeoutParam := r.URL.Query().Get("timeout")
		if timeoutParam != "20" {
			t.Errorf("expected timeout '20', got '%s'", timeoutParam)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success": true, "url": "https://stream.vidfast.vc/126560.mp4"}`))
	}))
	defer extractorTS.Close()

	// Mock Worker Server
	workerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&workerCalled, 1)
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"url":        extractorTS.URL + "/", // Trailing slash must be trimmed
			"expires_at": time.Now().Add(1 * time.Hour).Unix(),
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer workerTS.Close()

	tier := NewExtractorTier(workerTS.URL, "https://vidfast.vc", 2*time.Second)
	res, err := tier.ResolveMovie(context.Background(), 126560)
	if err != nil || res == nil {
		t.Fatalf("expected resolution, got err: %v, res: %+v", err, res)
	}

	if atomic.LoadInt32(&workerCalled) != 1 {
		t.Errorf("expected worker to be called exactly 1 time, got %d", workerCalled)
	}
	if atomic.LoadInt32(&extractorCalled) != 1 {
		t.Errorf("expected extractor to be called exactly 1 time, got %d", extractorCalled)
	}

	if !res.Success || res.Tier != 2 || res.Source != "vidfast-extractor" || res.URL != "https://stream.vidfast.vc/126560.mp4" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Headers["Referer"] != "https://vidfast.vc/" || res.Headers["Origin"] != "https://vidfast.vc" {
		t.Errorf("headers must preserve VidFast base URL, got: %+v", res.Headers)
	}
}

// 2. Verify TV show resolution
func TestExtractorTier_ResolveTV_Success(t *testing.T) {
	extractorTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := r.URL.Query().Get("url")
		if target != "https://vidfast.vc/tv/444/3/2" {
			t.Errorf("expected url query param 'https://vidfast.vc/tv/444/3/2', got '%s'", target)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success": true, "url": "https://stream.vidfast.vc/tv444s3e2.mp4"}`))
	}))
	defer extractorTS.Close()

	workerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"url":        extractorTS.URL,
			"expires_at": time.Now().Add(1 * time.Hour).Unix(),
		})
	}))
	defer workerTS.Close()

	tier := NewExtractorTier(workerTS.URL, "https://vidfast.vc", 2*time.Second)
	res, err := tier.ResolveTV(context.Background(), 444, 3, 2)
	if err != nil || res == nil {
		t.Fatalf("expected TV resolution, got err: %v, res: %+v", err, res)
	}

	if !res.Success || res.Tier != 2 || *res.Season != 3 || *res.Episode != 2 {
		t.Fatalf("unexpected TV result: %+v", res)
	}
}

// 3. Verify cached URL reuse (do not call worker repeatedly)
func TestExtractorTier_CachedURLReuse(t *testing.T) {
	var workerCount int32
	extractorTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success": true, "url": "https://stream.vidfast.vc/movie.mp4"}`))
	}))
	defer extractorTS.Close()

	workerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&workerCount, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"url":        extractorTS.URL,
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),
		})
	}))
	defer workerTS.Close()

	tier := NewExtractorTier(workerTS.URL, "https://vidfast.vc", 2*time.Second)

	// Call 1: cold cache -> worker is called
	res1, _ := tier.ResolveMovie(context.Background(), 100)
	if res1 == nil || !res1.Success {
		t.Fatalf("expected res1 success")
	}

	// Call 2: warm cache -> worker MUST NOT be called again
	res2, _ := tier.ResolveMovie(context.Background(), 101)
	if res2 == nil || !res2.Success {
		t.Fatalf("expected res2 success")
	}

	// Call 3: warm cache -> worker MUST NOT be called again
	res3, _ := tier.ResolveMovie(context.Background(), 102)
	if res3 == nil || !res3.Success {
		t.Fatalf("expected res3 success")
	}

	if count := atomic.LoadInt32(&workerCount); count != 1 {
		t.Errorf("worker should only be called once, got %d calls", count)
	}
}

// 4. Verify fallback caching duration when expires_at is omitted
func TestExtractorTier_MissingExpiresAt_UsesFallbackCacheDuration(t *testing.T) {
	extractorTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success": true, "url": "https://stream.vidfast.vc/movie.mp4"}`))
	}))
	defer extractorTS.Close()

	workerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"url": extractorTS.URL, // no expires_at
		})
	}))
	defer workerTS.Close()

	tier := NewExtractorTier(workerTS.URL, "https://vidfast.vc", 2*time.Second)
	_, _ = tier.ResolveMovie(context.Background(), 200)

	cachedURL, expiresAt := tier.GetCachedExtractorURL()
	if cachedURL != extractorTS.URL {
		t.Errorf("expected cached URL %s, got %s", extractorTS.URL, cachedURL)
	}
	if expiresAt.Before(time.Now().Add(4 * time.Minute)) {
		t.Errorf("expected fallback cache expiration ~5m into future, got %v", expiresAt)
	}
}

// 5. Verify expiry-based refresh (within 5-minute safety margin)
func TestExtractorTier_ExpiryBasedRefresh(t *testing.T) {
	var workerCount int32
	extractorTS1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success": true, "url": "https://stream.vidfast.vc/stream1.mp4"}`))
	}))
	defer extractorTS1.Close()

	extractorTS2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success": true, "url": "https://stream.vidfast.vc/stream2.mp4"}`))
	}))
	defer extractorTS2.Close()

	workerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := atomic.AddInt32(&workerCount, 1)
		w.Header().Set("Content-Type", "application/json")
		if c == 1 {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"url":        extractorTS1.URL,
				"expires_at": time.Now().Add(20 * time.Minute).Unix(),
			})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"url":        extractorTS2.URL,
				"expires_at": time.Now().Add(60 * time.Minute).Unix(),
			})
		}
	}))
	defer workerTS.Close()

	tier := NewExtractorTier(workerTS.URL, "https://vidfast.vc", 2*time.Second)

	// Call 1: initial discovery gets extractorTS1
	res1, _ := tier.ResolveMovie(context.Background(), 100)
	if res1 == nil || res1.URL != "https://stream.vidfast.vc/stream1.mp4" {
		t.Fatalf("expected stream1 from extractor 1, got %+v", res1)
	}

	// Artificially simulate that current cached URL expires in 3 minutes (less than the 5-minute safety threshold)
	tier.SetCachedExtractorURL(extractorTS1.URL, time.Now().Add(3*time.Minute))

	// Call 2: should trigger automatic refresh from worker and switch to extractorTS2
	res2, _ := tier.ResolveMovie(context.Background(), 101)
	if res2 == nil || res2.URL != "https://stream.vidfast.vc/stream2.mp4" {
		t.Fatalf("expected stream2 from extractor 2 after expiry refresh, got %+v", res2)
	}

	if count := atomic.LoadInt32(&workerCount); count != 2 {
		t.Errorf("expected worker to be called 2 times (initial + refresh), got %d", count)
	}
}

// 6. Verify refresh after extractor connection failure and single retry
func TestExtractorTier_RefreshAfterExtractorConnectionFailure_And_SingleRetry(t *testing.T) {
	var workerCount int32
	var extractor2Called int32

	// Extractor 1 fails (closes immediately or returns 502/connection error)
	extractorTS1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "tunnel connection lost", http.StatusBadGateway)
	}))
	defer extractorTS1.Close()

	// Extractor 2 succeeds
	extractorTS2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&extractor2Called, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success": true, "url": "https://stream.vidfast.vc/recovered.mp4"}`))
	}))
	defer extractorTS2.Close()

	workerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := atomic.AddInt32(&workerCount, 1)
		w.Header().Set("Content-Type", "application/json")
		if c == 1 {
			// First return dead extractorTS1
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"url":        extractorTS1.URL,
				"expires_at": time.Now().Add(30 * time.Minute).Unix(),
			})
		} else {
			// On refresh return working extractorTS2
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"url":        extractorTS2.URL,
				"expires_at": time.Now().Add(30 * time.Minute).Unix(),
			})
		}
	}))
	defer workerTS.Close()

	tier := NewExtractorTier(workerTS.URL, "https://vidfast.vc", 2*time.Second)

	res, err := tier.ResolveMovie(context.Background(), 550)
	if err != nil || res == nil {
		t.Fatalf("expected recovery on retry, got err: %v, res: %+v", err, res)
	}

	if res.URL != "https://stream.vidfast.vc/recovered.mp4" {
		t.Errorf("expected recovered stream URL, got %s", res.URL)
	}

	if atomic.LoadInt32(&workerCount) != 2 {
		t.Errorf("expected 2 worker calls (initial + force-refresh), got %d", workerCount)
	}
	if atomic.LoadInt32(&extractor2Called) != 1 {
		t.Errorf("expected extractor 2 to be called on retry")
	}
}

// 7. Verify only ONE retry occurs on repeated failure, cascading to Tier 3 fallback
func TestExtractorTier_OnlyOneRetryOnPersistentFailure_CascadesTier3(t *testing.T) {
	var workerCount int32
	var extractorAttempts int32

	extractorTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&extractorAttempts, 1)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer extractorTS.Close()

	workerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&workerCount, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"url":        extractorTS.URL,
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),
		})
	}))
	defer workerTS.Close()

	tier := NewExtractorTier(workerTS.URL, "https://vidfast.vc", 2*time.Second)

	// Movie resolution should return nil, nil so Tier 3 fallback occurs
	res, err := tier.ResolveMovie(context.Background(), 999)
	if err != nil {
		t.Errorf("expected nil error for Tier 3 cascade, got %v", err)
	}
	if res != nil {
		t.Errorf("expected nil result on total failure, got %+v", res)
	}

	// Should attempt initial (1) + retry (1) = exactly 2 extractor requests
	if attempts := atomic.LoadInt32(&extractorAttempts); attempts != 2 {
		t.Errorf("expected exactly 2 extractor attempts (initial + 1 retry), got %d", attempts)
	}
	// Worker should be called: initial (1) + force-refresh (1) = 2
	if wc := atomic.LoadInt32(&workerCount); wc != 2 {
		t.Errorf("expected exactly 2 worker calls, got %d", wc)
	}
}

// 8. Verify concurrent worker requests are deduplicated
func TestExtractorTier_ConcurrentRequests_DeduplicateWorkerCalls(t *testing.T) {
	var workerCalls int32
	extractorTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success": true, "url": "https://stream.vidfast.vc/concurrent.mp4"}`))
	}))
	defer extractorTS.Close()

	workerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&workerCalls, 1)
		time.Sleep(50 * time.Millisecond) // Simulate network delay
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"url":        extractorTS.URL,
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),
		})
	}))
	defer workerTS.Close()

	tier := NewExtractorTier(workerTS.URL, "https://vidfast.vc", 2*time.Second)

	var wg sync.WaitGroup
	const concurrency = 10
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			defer wg.Done()
			res, err := tier.ResolveMovie(context.Background(), 100+id)
			if err != nil || res == nil || !res.Success {
				t.Errorf("goroutine %d failed: res=%+v, err=%v", id, res, err)
			}
		}(i)
	}

	wg.Wait()

	if calls := atomic.LoadInt32(&workerCalls); calls != 1 {
		t.Errorf("expected exactly 1 worker call across 10 concurrent requests, got %d", calls)
	}
}

// 9. Verify application-level failure (e.g. success: false) does not retry and cascades cleanly
func TestExtractorTier_ApplicationFailure_NoRetry_CascadesTier3(t *testing.T) {
	var extractorAttempts int32
	extractorTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&extractorAttempts, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success": false, "message": "media not found on vidfast"}`))
	}))
	defer extractorTS.Close()

	workerTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"url":        extractorTS.URL,
			"expires_at": time.Now().Add(30 * time.Minute).Unix(),
		})
	}))
	defer workerTS.Close()

	tier := NewExtractorTier(workerTS.URL, "https://vidfast.vc", 2*time.Second)
	res, err := tier.ResolveMovie(context.Background(), 999)
	if err != nil {
		t.Errorf("expected nil error to cascade to Tier 3, got: %v", err)
	}
	if res != nil {
		t.Errorf("expected nil result on not-found, got %+v", res)
	}

	// Should not retry on application-level success=false
	if attempts := atomic.LoadInt32(&extractorAttempts); attempts != 1 {
		t.Errorf("expected exactly 1 attempt on application failure, got %d", attempts)
	}
}
