package handlers

import (
	"context"
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

type mockTier struct {
	movieFn func(ctx context.Context, id int) (*models.ResolutionResult, error)
	tvFn    func(ctx context.Context, id, s, e int) (*models.ResolutionResult, error)
}

func (m *mockTier) ResolveMovie(ctx context.Context, id int) (*models.ResolutionResult, error) {
	if m.movieFn != nil {
		return m.movieFn(ctx, id)
	}
	return nil, nil
}

func (m *mockTier) ResolveTV(ctx context.Context, id, s, e int) (*models.ResolutionResult, error) {
	if m.tvFn != nil {
		return m.tvFn(ctx, id, s, e)
	}
	return nil, nil
}

func setupTestServer() (*httptest.Server, *tiers.VidaraTier) {
	t1 := tiers.NewVidaraTier("https://vidara.to", "https://streamtape.com", 2*time.Second)
	_ = t1.LoadData("data/vidara.json", "data.json", "../data/vidara.json", "../data.json")

	// Mock Tier 2 that resolves movie 126560 and tv 444/3/2
	t2 := &mockTier{
		movieFn: func(ctx context.Context, id int) (*models.ResolutionResult, error) {
			if id == 126560 {
				return &models.ResolutionResult{
					Success: true,
					TmdbID:  id,
					Type:    "movie",
					Tier:    2,
					Source:  "vidfast-extractor",
					URL:     "https://stream.vidfast.vc/126560.mp4",
				}, nil
			}
			return nil, nil
		},
		tvFn: func(ctx context.Context, id, s, e int) (*models.ResolutionResult, error) {
			if id == 444 && s == 3 && e == 2 {
				return &models.ResolutionResult{
					Success: true,
					TmdbID:  id,
					Type:    "tv",
					Tier:    2,
					Source:  "vidfast-extractor",
					Season:  &s,
					Episode: &e,
					URL:     "https://stream.vidfast.vc/tv444s3e2.mp4",
				}, nil
			}
			return nil, nil
		},
	}

	// Mock Tier 3 that resolves movie 550 and tv 1399/1/6
	t3 := &mockTier{
		movieFn: func(ctx context.Context, id int) (*models.ResolutionResult, error) {
			if id == 550 {
				return &models.ResolutionResult{
					Success: true,
					TmdbID:  id,
					Type:    "movie",
					Tier:    3,
					Source:  "showbox",
					Title:   "Fight Club",
					URL:     "https://hls.shegu.net/550.m3u8",
					Headers: map[string]string{
						"Origin":  "https://www.febbox.com",
						"Referer": "https://www.febbox.com/",
					},
				}, nil
			}
			return nil, nil
		},
		tvFn: func(ctx context.Context, id, s, e int) (*models.ResolutionResult, error) {
			if id == 1399 && s == 1 && e == 6 {
				return &models.ResolutionResult{
					Success: true,
					TmdbID:  id,
					Type:    "tv",
					Tier:    3,
					Source:  "showbox",
					Title:   "Game of Thrones",
					Season:  &s,
					Episode: &e,
					URL:     "https://hls.shegu.net/got_s1e6.m3u8",
					Headers: map[string]string{
						"Origin":  "https://www.febbox.com",
						"Referer": "https://www.febbox.com/",
					},
				}, nil
			}
			return nil, nil
		},
	}

	r := resolver.NewCentralResolver(t1, t2, t3)
	h := NewAPIHandler(r, t1, nil, 2*time.Second)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", h.HandleHealth)
	mux.HandleFunc("/api/movie/", h.HandleMovie)
	mux.HandleFunc("/api/tv/", h.HandleTV)

	return httptest.NewServer(mux), t1
}

func TestHealthEndpoint(t *testing.T) {
	ts, _ := setupTestServer()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var data map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&data)
	if data["status"] != "ok" {
		t.Errorf("expected status 'ok', got '%v'", data["status"])
	}
}

func TestMovieEndpoints_AllTiers(t *testing.T) {
	ts, _ := setupTestServer()
	defer ts.Close()

	// 1. Movie Tier 1: 1167307
	resp1, err := http.Get(ts.URL + "/api/movie/1167307")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp1.StatusCode)
	}
	var res1 models.ResolutionResult
	_ = json.NewDecoder(resp1.Body).Decode(&res1)
	if !res1.Success || res1.Tier != 1 || res1.Source != "vidara" || res1.TmdbID != 1167307 {
		t.Errorf("expected Tier 1 Vidara for 1167307, got %+v", res1)
	}

	// 2. Movie Tier 2 fallback: 126560
	resp2, err := http.Get(ts.URL + "/api/movie/126560")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}
	var res2 models.ResolutionResult
	_ = json.NewDecoder(resp2.Body).Decode(&res2)
	if !res2.Success || res2.Tier != 2 || res2.Source != "vidfast-extractor" {
		t.Errorf("expected Tier 2 for 126560, got %+v", res2)
	}

	// 3. Movie Tier 3 fallback: 550
	resp3, err := http.Get(ts.URL + "/api/movie/550")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp3.StatusCode)
	}
	var res3 models.ResolutionResult
	_ = json.NewDecoder(resp3.Body).Decode(&res3)
	if !res3.Success || res3.Tier != 3 || res3.Source != "showbox" {
		t.Errorf("expected Tier 3 for 550, got %+v", res3)
	}
	if res3.Headers["Origin"] != "https://www.febbox.com" || res3.Headers["Referer"] != "https://www.febbox.com/" {
		t.Errorf("expected febbox headers, got %+v", res3.Headers)
	}

	// 4. Movie Complete Failure: 9999999
	resp4, err := http.Get(ts.URL + "/api/movie/9999999")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 NotFound, got %d", resp4.StatusCode)
	}
	var res4 models.ResolutionResult
	_ = json.NewDecoder(resp4.Body).Decode(&res4)
	if res4.Success || res4.Error != "source_not_found" {
		t.Errorf("expected clean 404 failure structure, got %+v", res4)
	}

	// 5. Validation failures: 0, negative, string
	for _, badID := range []string{"0", "-1", "abc"} {
		respBad, _ := http.Get(ts.URL + "/api/movie/" + badID)
		if respBad.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for id %s, got %d", badID, respBad.StatusCode)
		}
	}
}

func TestTVEndpoints_AllTiers(t *testing.T) {
	ts, _ := setupTestServer()
	defer ts.Close()

	// 1. TV Tier 1: 262838 Season 1 Episode 2
	resp1, err := http.Get(ts.URL + "/api/tv/262838/1/2")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp1.StatusCode)
	}
	var res1 models.ResolutionResult
	_ = json.NewDecoder(resp1.Body).Decode(&res1)
	if !res1.Success || res1.Tier != 1 || *res1.Season != 1 || *res1.Episode != 2 {
		t.Errorf("expected Tier 1 TV S1E2, got %+v", res1)
	}

	// 2. TV Tier 2 fallback: 444 Season 3 Episode 2
	resp2, err := http.Get(ts.URL + "/api/tv/444/3/2")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}
	var res2 models.ResolutionResult
	_ = json.NewDecoder(resp2.Body).Decode(&res2)
	if !res2.Success || res2.Tier != 2 || *res2.Season != 3 || *res2.Episode != 2 {
		t.Errorf("expected Tier 2 TV S3E2, got %+v", res2)
	}

	// 3. TV Tier 3 fallback: 1399 Season 1 Episode 6
	resp3, err := http.Get(ts.URL + "/api/tv/1399/1/6")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp3.StatusCode)
	}
	var res3 models.ResolutionResult
	_ = json.NewDecoder(resp3.Body).Decode(&res3)
	if !res3.Success || res3.Tier != 3 || *res3.Season != 1 || *res3.Episode != 6 {
		t.Errorf("expected Tier 3 TV S1E6, got %+v", res3)
	}

	// 4. TV Complete Failure: 999 Season 1 Episode 1
	resp4, err := http.Get(ts.URL + "/api/tv/999/1/1")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp4.StatusCode)
	}
	var res4 models.ResolutionResult
	_ = json.NewDecoder(resp4.Body).Decode(&res4)
	if res4.Success || res4.Error != "source_not_found" {
		t.Errorf("expected source_not_found, got %+v", res4)
	}

	// 5. TV Validation: invalid route or values
	badRoutes := []string{
		"/api/tv/444/0/2",
		"/api/tv/444/1/0",
		"/api/tv/0/1/1",
		"/api/tv/444/1",
		"/api/tv/abc/1/1",
	}
	for _, br := range badRoutes {
		respBad, _ := http.Get(ts.URL + br)
		if respBad.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 for bad route %s, got %d", br, respBad.StatusCode)
		}
	}
}

// ============================================================================
// PART C: CLOUDFLARE WORKER PROXY INTEGRATION TESTS IN GO
// ============================================================================

func TestAPI_MovieWithWorkerProxy_ReplacesURL_AndClearsHeaders(t *testing.T) {
	// Mock Cloudflare Worker
	workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req proxy.SessionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(proxy.SessionResponse{
			Success:     true,
			SessionID:   "sess-cf-550",
			PlaybackURL: "http://" + r.Host + "/session/sess-cf-550/master.m3u8",
		})
	}))
	defer workerServer.Close()

	t3 := &mockTier{
		movieFn: func(ctx context.Context, id int) (*models.ResolutionResult, error) {
			return &models.ResolutionResult{
				Success: true,
				TmdbID:  id,
				Type:    "movie",
				Tier:    3,
				Source:  "showbox",
				Title:   "Fight Club",
				URL:     "https://hls.shegu.net/550.m3u8",
				Headers: map[string]string{
					"Origin":  "https://www.febbox.com",
					"Referer": "https://www.febbox.com/",
				},
			}, nil
		},
	}

	r := resolver.NewCentralResolver(nil, nil, t3)
	h := NewAPIHandler(r, nil, nil, 2*time.Second)
	wc := proxy.NewWorkerClient([]string{workerServer.URL}, "", 2*time.Second)
	h.SetWorkerClient(wc, false)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/movie/", h.HandleMovie)
	server := httptest.NewServer(mux)
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/movie/550")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var res models.ResolutionResult
	_ = json.NewDecoder(resp.Body).Decode(&res)

	// Verify Worker URL replaced upstream URL
	if !strings.Contains(res.URL, "/session/sess-cf-550/master.m3u8") {
		t.Errorf("expected worker playback URL, got: %s", res.URL)
	}

	// Verify upstream Origin and Referer are NOT exposed to client
	if res.Headers["Origin"] != "" || res.Headers["Referer"] != "" {
		t.Errorf("expected upstream headers to be cleared, got: %+v", res.Headers)
	}

	// Verify tier and source metadata remain intact
	if res.Tier != 3 || res.Source != "showbox" || res.TmdbID != 550 {
		t.Errorf("expected source metadata intact, got: %+v", res)
	}
}

func TestAPI_MovieWithWorkerFailover(t *testing.T) {
	// Worker 1 fails
	w1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer w1.Close()

	// Worker 2 succeeds
	w2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(proxy.SessionResponse{
			Success:     true,
			SessionID:   "sess-w2-success",
			PlaybackURL: "http://" + r.Host + "/session/sess-w2-success/master.m3u8",
		})
	}))
	defer w2.Close()

	t3 := &mockTier{
		movieFn: func(ctx context.Context, id int) (*models.ResolutionResult, error) {
			return &models.ResolutionResult{
				Success: true,
				TmdbID:  id,
				Type:    "movie",
				Tier:    3,
				Source:  "vidrift-atlas",
				URL:     "https://upstream.cdn/550/master.m3u8",
			}, nil
		},
	}

	r := resolver.NewCentralResolver(nil, nil, t3)
	h := NewAPIHandler(r, nil, nil, 2*time.Second)
	wc := proxy.NewWorkerClient([]string{w1.URL, w2.URL}, "", 2*time.Second)
	h.SetWorkerClient(wc, false)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/movie/", h.HandleMovie)
	server := httptest.NewServer(mux)
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/movie/550")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var res models.ResolutionResult
	_ = json.NewDecoder(resp.Body).Decode(&res)

	if !strings.Contains(res.URL, "/session/sess-w2-success/master.m3u8") {
		t.Errorf("expected Worker 2 URL, got: %s", res.URL)
	}
}

func TestAPI_MovieWithAllWorkersFailing_Returns502(t *testing.T) {
	w1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer w1.Close()

	t3 := &mockTier{
		movieFn: func(ctx context.Context, id int) (*models.ResolutionResult, error) {
			return &models.ResolutionResult{
				Success: true,
				TmdbID:  id,
				Type:    "movie",
				Tier:    3,
				URL:     "https://upstream.cdn/550.m3u8",
			}, nil
		},
	}

	r := resolver.NewCentralResolver(nil, nil, t3)
	h := NewAPIHandler(r, nil, nil, 2*time.Second)
	wc := proxy.NewWorkerClient([]string{w1.URL}, "", 2*time.Second)
	h.SetWorkerClient(wc, false) // fallbackDirect = false

	mux := http.NewServeMux()
	mux.HandleFunc("/api/movie/", h.HandleMovie)
	server := httptest.NewServer(mux)
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/movie/550")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 Bad Gateway when all workers fail, got %d", resp.StatusCode)
	}

	var data map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&data)
	if data["error"] != "proxy_workers_failed" {
		t.Errorf("expected error 'proxy_workers_failed', got: %v", data["error"])
	}
}

func TestAPI_M3UWithWorkerProxy(t *testing.T) {
	workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(proxy.SessionResponse{
			Success:     true,
			SessionID:   "sess-m3u",
			PlaybackURL: "http://" + r.Host + "/session/sess-m3u/master.m3u8",
		})
	}))
	defer workerServer.Close()

	t3 := &mockTier{
		movieFn: func(ctx context.Context, id int) (*models.ResolutionResult, error) {
			return &models.ResolutionResult{
				Success: true,
				TmdbID:  id,
				Type:    "movie",
				Tier:    3,
				Title:   "Fight Club",
				URL:     "https://upstream.cdn/550.m3u8",
				Headers: map[string]string{
					"Origin":  "https://upstream.cdn",
					"Referer": "https://upstream.cdn/play",
				},
			}, nil
		},
	}

	r := resolver.NewCentralResolver(nil, nil, t3)
	h := NewAPIHandler(r, nil, nil, 2*time.Second)
	wc := proxy.NewWorkerClient([]string{workerServer.URL}, "", 2*time.Second)
	h.SetWorkerClient(wc, false)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/movie/", h.HandleMovie)
	server := httptest.NewServer(mux)
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/movie/550?format=m3u")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	m3uText := string(body)

	// M3U should contain Worker playback URL
	if !strings.Contains(m3uText, "/session/sess-m3u/master.m3u8") {
		t.Errorf("expected M3U to contain worker URL, got: %s", m3uText)
	}

	// M3U should NOT contain upstream Origin/Referer directives
	if strings.Contains(m3uText, "#EXTVLCOPT:http-referrer=https://upstream.cdn/play") {
		t.Errorf("expected M3U to strip upstream Referer, but found it in: %s", m3uText)
	}
}

func TestAPI_DirectMP4_BypassesWorkerProxy(t *testing.T) {
	workerCalled := false
	workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workerCalled = true
		http.Error(w, "worker should not be called for direct mp4", http.StatusInternalServerError)
	}))
	defer workerServer.Close()

	t3 := &mockTier{
		movieFn: func(ctx context.Context, id int) (*models.ResolutionResult, error) {
			return &models.ResolutionResult{
				Success:    true,
				TmdbID:     id,
				Tier:       3,
				Source:     "cinetv",
				Title:      "Fight Club",
				URL:        "https://filmin.a0p3.com/vod/1/fightclub.mp4?wsSecret=abc",
				Direct:     true,
				StreamType: "mp4",
			}, nil
		},
	}

	r := resolver.NewCentralResolver(nil, nil, t3)
	h := NewAPIHandler(r, nil, nil, 2*time.Second)
	wc := proxy.NewWorkerClient([]string{workerServer.URL}, "", 2*time.Second)
	h.SetWorkerClient(wc, false)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/movie/", h.HandleMovie)
	server := httptest.NewServer(mux)
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/movie/550")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	if workerCalled {
		t.Errorf("Cloudflare worker proxy should NOT be called for direct MP4!")
	}

	var data map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&data)
	if data["url"] != "https://filmin.a0p3.com/vod/1/fightclub.mp4?wsSecret=abc" {
		t.Errorf("expected direct MP4 URL, got: %v", data["url"])
	}
	if data["source"] != "cinetv" {
		t.Errorf("expected source 'cinetv', got: %v", data["source"])
	}
}
