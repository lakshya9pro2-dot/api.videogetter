package handlers

import (
	"context"
	"encoding/json"
	"fmt"
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

// TestWebProxy_NormalMode_Only_Streamtape_Proxied verifies that
// in normal mode (without ?web), Vidara, HubCloud, Direct, Tier 2, Tier 3, Tier 4, and StreamHG are NOT proxied,
// while ONLY Streamtape is proxied (via Netlify proxy worker).
func TestWebProxy_NormalMode_Only_Streamtape_Proxied(t *testing.T) {
	workerCalls := 0
	workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workerCalls++
		var req proxy.SessionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(proxy.SessionResponse{
			Success:     true,
			SessionID:   "sess-worker",
			PlaybackURL: "http://" + r.Host + "/session/sess-worker/master.m3u8",
		})
	}))
	defer workerServer.Close()

	hubCloudWorkerCalls := 0
	hubCloudServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hubCloudWorkerCalls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tiers.HubCloudHideResponse{
			StreamURL: "/stream/hubcloud.mkv",
		})
	}))
	defer hubCloudServer.Close()

	vidaraTier := tiers.NewVidaraTier("https://vidara.to", "https://streamtape.com", 2*time.Second)
	vidaraTier.SetHubCloudProxyURL(hubCloudServer.URL)
	wc := proxy.NewWorkerClient([]string{workerServer.URL}, "", 2*time.Second)

	testCases := []struct {
		name          string
		source        string
		rawURL        string
		streamType    string
		expectProxied bool
	}{
		{"Vidara", "vidara", "https://vidara.to/stream/123.m3u8", "m3u8", false},
		{"HubCloud", "hubcloud", "https://hubcloud.cdn/download/stream.mkv", "mkv", false},
		{"Direct", "direct", "http://192.168.1.8/video.mp4", "mp4", false},
		{"Tier2-VidFast", "vidfast-extractor", "https://vidfast.vc/stream/456.m3u8", "m3u8", false},
		{"Tier3-Vidrift", "vidrift-atlas", "https://edge.reelvault.click/proxy?u=789", "m3u8", false},
		{"Tier4-ShowBox", "showbox", "https://hls.shegu.net/999.m3u8", "m3u8", false},
		{"StreamHG", "streamhg", "https://hanerix.com/stream/sh123.m3u8", "m3u8", true},
		{"Streamtape", "streamtape", "https://streamtape.com/get_video?id=st123", "mp4", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mt := &mockTier{
				movieFn: func(ctx context.Context, id int) (*models.ResolutionResult, error) {
					return &models.ResolutionResult{
						Success:    true,
						TmdbID:     id,
						Type:       "movie",
						Tier:       1,
						Source:     tc.source,
						Title:      "Test Movie",
						URL:        tc.rawURL,
						StreamType: tc.streamType,
						Headers: map[string]string{
							"Origin":  "https://test.origin",
							"Referer": "https://test.referer/",
						},
					}, nil
				},
			}

			r := resolver.NewCentralResolver(mt, nil, nil)
			h := NewAPIHandler(r, vidaraTier, nil, 2*time.Second)
			h.SetWorkerClient(wc, false)

			mux := http.NewServeMux()
			mux.HandleFunc("/api/movie/", h.HandleMovie)
			srv := httptest.NewServer(mux)
			defer srv.Close()

			resp, err := http.Get(srv.URL + "/api/movie/550")
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			var res models.ResolutionResult
			_ = json.NewDecoder(resp.Body).Decode(&res)

			if tc.expectProxied {
				if !strings.Contains(res.URL, "/session/") {
					t.Errorf("[%s] expected proxied worker URL, got %s", tc.name, res.URL)
				}
				if res.Headers["Origin"] != "" {
					t.Errorf("[%s] expected headers to be cleared, got %+v", tc.name, res.Headers)
				}
			} else {
				if res.URL != tc.rawURL {
					t.Errorf("[%s] expected direct URL %q without proxy, got %q", tc.name, tc.rawURL, res.URL)
				}
				if tc.source != "direct" && tc.source != "hubcloud" && res.Headers["Origin"] != "https://test.origin" {
					t.Errorf("[%s] expected headers to be preserved, got %+v", tc.name, res.Headers)
				}
			}
		})
	}
}

// TestWebProxy_WebMode_AllProxied verifies that with ?web, ALL sources are proxied.
func TestWebProxy_WebMode_AllProxied(t *testing.T) {
	workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req proxy.SessionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(proxy.SessionResponse{
			Success:     true,
			SessionID:   "sess-web",
			PlaybackURL: "http://" + r.Host + "/session/sess-web/master.m3u8",
		})
	}))
	defer workerServer.Close()

	hubCloudServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tiers.HubCloudHideResponse{
			StreamURL: "/stream/proxied_hubcloud.mkv",
		})
	}))
	defer hubCloudServer.Close()

	vidaraTier := tiers.NewVidaraTier("https://vidara.to", "https://streamtape.com", 2*time.Second)
	vidaraTier.SetHubCloudProxyURL(hubCloudServer.URL)
	wc := proxy.NewWorkerClient([]string{workerServer.URL}, "", 2*time.Second)

	testSources := []struct {
		name         string
		source       string
		rawURL       string
		streamType   string
		isStreamtape bool
	}{
		{"Vidara", "vidara", "https://vidara.to/stream/123.m3u8", "m3u8", false},
		{"HubCloud", "hubcloud", "https://hubcloud.cdn/download/stream.mkv", "mkv", false},
		{"Tier2-VidFast", "vidfast-extractor", "https://vidfast.vc/stream/456.m3u8", "m3u8", false},
		{"Tier3-Vidrift", "vidrift-atlas", "https://edge.reelvault.click/proxy?u=789", "m3u8", false},
		{"Tier4-ShowBox", "showbox", "https://hls.shegu.net/999.m3u8", "m3u8", false},
		{"StreamHG", "streamhg", "https://hanerix.com/stream/sh123.m3u8", "m3u8", true},
		{"Streamtape", "streamtape", "https://streamtape.com/get_video?id=st123", "mp4", true},
	}

	for _, tc := range testSources {
		t.Run(tc.name, func(t *testing.T) {
			mt := &mockTier{
				movieFn: func(ctx context.Context, id int) (*models.ResolutionResult, error) {
					return &models.ResolutionResult{
						Success:    true,
						TmdbID:     id,
						Type:       "movie",
						Tier:       1,
						Source:     tc.source,
						Title:      "Test Movie",
						URL:        tc.rawURL,
						StreamType: tc.streamType,
						Headers: map[string]string{
							"Origin":  "https://test.origin",
							"Referer": "https://test.referer/",
						},
					}, nil
				},
				tvFn: func(ctx context.Context, tmdbID, season, episode int) (*models.ResolutionResult, error) {
					return &models.ResolutionResult{
						Success:    true,
						TmdbID:     tmdbID,
						Type:       "tv",
						Tier:       1,
						Source:     tc.source,
						Title:      "Test Show",
						Season:     &season,
						Episode:    &episode,
						URL:        tc.rawURL,
						StreamType: tc.streamType,
						Headers: map[string]string{
							"Origin":  "https://test.origin",
							"Referer": "https://test.referer/",
						},
					}, nil
				},
			}

			r := resolver.NewCentralResolver(mt, nil, nil)
			h := NewAPIHandler(r, vidaraTier, nil, 2*time.Second)
			h.SetWorkerClient(wc, false)

			mux := http.NewServeMux()
			mux.HandleFunc("/api/movie/", h.HandleMovie)
			mux.HandleFunc("/api/tv/", h.HandleTV)
			srv := httptest.NewServer(mux)
			defer srv.Close()

			// Test movie with ?web
			resp, err := http.Get(srv.URL + "/api/movie/550?web")
			if err != nil {
				t.Fatalf("movie ?web request failed: %v", err)
			}
			defer resp.Body.Close()

			var res models.ResolutionResult
			_ = json.NewDecoder(resp.Body).Decode(&res)

			if tc.isStreamtape || tc.source == "streamhg" {
				if !strings.Contains(res.URL, "/session/sess-web/") {
					t.Errorf("[%s Movie] expected Netlify Worker proxied URL, got %s", tc.name, res.URL)
				}
			} else {
				if !strings.Contains(res.URL, "/stream/proxied_hubcloud.mkv") {
					t.Errorf("[%s Movie] expected Proxyservr Worker proxied URL, got %s", tc.name, res.URL)
				}
			}

			// Test TV with ?web
			respTV, err := http.Get(srv.URL + "/api/tv/1396/1/1?web")
			if err != nil {
				t.Fatalf("tv ?web request failed: %v", err)
			}
			defer respTV.Body.Close()

			var resTV models.ResolutionResult
			_ = json.NewDecoder(respTV.Body).Decode(&resTV)

			if tc.isStreamtape || tc.source == "streamhg" {
				if !strings.Contains(resTV.URL, "/session/sess-web/") {
					t.Errorf("[%s TV] expected Netlify Worker proxied URL, got %s", tc.name, resTV.URL)
				}
			} else {
				if !strings.Contains(resTV.URL, "/stream/proxied_hubcloud.mkv") {
					t.Errorf("[%s TV] expected Proxyservr Worker proxied URL, got %s", tc.name, resTV.URL)
				}
			}
		})
	}
}

// TestWebProxy_QueryParamVariations tests various truthy and falsy representations of ?web.
func TestWebProxy_QueryParamVariations(t *testing.T) {
	workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/hide") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(tiers.HubCloudHideResponse{
				StreamURL: "/stream/sess-param-test.m3u8",
			})
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(proxy.SessionResponse{
			Success:     true,
			SessionID:   "sess-param-test",
			PlaybackURL: "http://" + r.Host + "/session/sess-param-test/master.m3u8",
		})
	}))
	defer workerServer.Close()

	rawTarget := "https://vidara.to/stream/direct.m3u8"
	mt := &mockTier{
		movieFn: func(ctx context.Context, id int) (*models.ResolutionResult, error) {
			return &models.ResolutionResult{
				Success: true,
				TmdbID:  id,
				Type:    "movie",
				Source:  "vidara",
				URL:     rawTarget,
			}, nil
		},
	}

	r := resolver.NewCentralResolver(mt, nil, nil)
	h := NewAPIHandler(r, nil, nil, 2*time.Second)
	wc := proxy.NewWorkerClient([]string{workerServer.URL}, "", 2*time.Second)
	h.SetWorkerClient(wc, false)
	h.SetWebWorkerURL(workerServer.URL)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/movie/", h.HandleMovie)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cases := []struct {
		query         string
		expectProxied bool
	}{
		{"", false},           // normal mode -> direct
		{"?web", true},        // ?web -> proxied
		{"?web=1", true},      // ?web=1 -> proxied
		{"?web=true", true},   // ?web=true -> proxied
		{"?web=yes", true},    // ?web=yes -> proxied
		{"?web=0", false},     // ?web=0 -> direct
		{"?web=false", false}, // ?web=false -> direct
		{"?web=no", false},    // ?web=no -> direct
	}

	for _, c := range cases {
		url := fmt.Sprintf("%s/api/movie/550%s", srv.URL, c.query)
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("request %s failed: %v", c.query, err)
		}
		var res models.ResolutionResult
		_ = json.NewDecoder(resp.Body).Decode(&res)
		_ = resp.Body.Close()

		if c.expectProxied {
			if !strings.Contains(res.URL, workerServer.URL) {
				t.Errorf("query %q: expected proxied URL, got %s", c.query, res.URL)
			}
		} else {
			if res.URL != rawTarget {
				t.Errorf("query %q: expected direct URL %q, got %q", c.query, rawTarget, res.URL)
			}
		}
	}
}

// TestWebProxy_DirectMP4_WithoutProxy_InBothModes verifies Direct MP4 streams play without proxy in both normal and web mode.
func TestWebProxy_DirectMP4_WithoutProxy_InBothModes(t *testing.T) {
	workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("worker server should NEVER be called for direct MP4!")
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer workerServer.Close()

	directURL := "https://cdn.videy.co/gzX5L28G1.mp4"
	mt := &mockTier{
		movieFn: func(ctx context.Context, id int) (*models.ResolutionResult, error) {
			return &models.ResolutionResult{
				Success:    true,
				TmdbID:     id,
				Type:       "movie",
				Tier:       1,
				Source:     "direct",
				Title:      "Direct MP4 Video",
				URL:        directURL,
				Direct:     true,
				StreamType: "mp4",
			}, nil
		},
	}

	r := resolver.NewCentralResolver(mt, nil, nil)
	h := NewAPIHandler(r, nil, nil, 2*time.Second)
	wc := proxy.NewWorkerClient([]string{workerServer.URL}, "", 2*time.Second)
	h.SetWorkerClient(wc, false)
	h.SetWebWorkerURL(workerServer.URL)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/movie/", h.HandleMovie)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 1. Test normal mode (without ?web)
	respNormal, err := http.Get(srv.URL + "/api/movie/550")
	if err != nil {
		t.Fatalf("normal request failed: %v", err)
	}
	defer respNormal.Body.Close()

	var resNormal models.ResolutionResult
	_ = json.NewDecoder(respNormal.Body).Decode(&resNormal)
	if resNormal.URL != directURL {
		t.Errorf("expected direct MP4 in normal mode: %q, got %q", directURL, resNormal.URL)
	}
	if !resNormal.Direct {
		t.Errorf("expected resNormal.Direct to be true")
	}

	// 2. Test web mode (?web)
	respWeb, err := http.Get(srv.URL + "/api/movie/550?web")
	if err != nil {
		t.Fatalf("web request failed: %v", err)
	}
	defer respWeb.Body.Close()

	var resWeb models.ResolutionResult
	_ = json.NewDecoder(respWeb.Body).Decode(&resWeb)
	if resWeb.URL != directURL {
		t.Errorf("expected direct MP4 in web mode (?web): %q, got %q", directURL, resWeb.URL)
	}
	if !resWeb.Direct {
		t.Errorf("expected resWeb.Direct to be true")
	}
}
