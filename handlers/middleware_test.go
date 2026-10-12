package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOriginFilterMiddleware(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/api/movie/550", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":true,"tmdb_id":550}`))
	})
	mux.HandleFunc("/api/movie/550.m3u", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("#EXTM3U\nstream.m3u8"))
	})
	mux.HandleFunc("/playlist.m3u", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("#EXTM3U\nplaylist"))
	})

	mw := OriginFilterMiddleware(mux, "https://api.kineflex.site", "https://api.kineflex.site/")

	// 1. Health check: passes unconditionally without redirect
	reqHealth := httptest.NewRequest("GET", "/health", nil)
	recHealth := httptest.NewRecorder()
	mw.ServeHTTP(recHealth, reqHealth)
	if recHealth.Code != http.StatusOK {
		t.Errorf("expected /health to return 200, got %d", recHealth.Code)
	}

	// 2. M3U side without origin: allowed directly for any player
	reqM3U := httptest.NewRequest("GET", "/api/movie/550.m3u", nil)
	recM3U := httptest.NewRecorder()
	mw.ServeHTTP(recM3U, reqM3U)
	if recM3U.Code != http.StatusOK {
		t.Errorf("expected M3U endpoint to return 200 without origin, got %d", recM3U.Code)
	}
	if recM3U.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected wildcard CORS for M3U player, got %s", recM3U.Header().Get("Access-Control-Allow-Origin"))
	}

	// 3. M3U with &t= query or path param: allowed directly
	reqM3UWithT := httptest.NewRequest("GET", "/api/movie/550.m3u&t=1791799262", nil)
	recM3UWithT := httptest.NewRecorder()
	mw.ServeHTTP(recM3UWithT, reqM3UWithT)
	// Since mux doesn't have exact route /api/movie/550.m3u&t=..., mux will return 404, but NOT 302 redirect
	if recM3UWithT.Code == http.StatusFound {
		t.Errorf("M3U request with &t= MUST NOT be redirected")
	}

	// 4. M3U with ?format=m3u: allowed directly
	reqM3UFormat := httptest.NewRequest("GET", "/api/movie/550?format=m3u", nil)
	recM3UFormat := httptest.NewRecorder()
	mw.ServeHTTP(recM3UFormat, reqM3UFormat)
	if recM3UFormat.Code != http.StatusOK {
		t.Errorf("expected format=m3u to pass without redirect, got %d", recM3UFormat.Code)
	}

	// 5. Main backend without Origin or Referer: MUST REDIRECT to https://api.kineflex.site/
	reqMainNoOrigin := httptest.NewRequest("GET", "/api/movie/550", nil)
	recMainNoOrigin := httptest.NewRecorder()
	mw.ServeHTTP(recMainNoOrigin, reqMainNoOrigin)
	if recMainNoOrigin.Code != http.StatusFound {
		t.Errorf("expected 302 redirect for direct backend access, got %d", recMainNoOrigin.Code)
	}
	if loc := recMainNoOrigin.Header().Get("Location"); loc != "https://api.kineflex.site/" {
		t.Errorf("expected redirect location https://api.kineflex.site/, got %s", loc)
	}

	// 6. Main backend with unauthorized origin: MUST REDIRECT
	reqMainBadOrigin := httptest.NewRequest("GET", "/api/movie/550", nil)
	reqMainBadOrigin.Header.Set("Origin", "https://unauthorized-domain.com")
	recMainBadOrigin := httptest.NewRecorder()
	mw.ServeHTTP(recMainBadOrigin, reqMainBadOrigin)
	if recMainBadOrigin.Code != http.StatusFound {
		t.Errorf("expected 302 redirect for unauthorized origin, got %d", recMainBadOrigin.Code)
	}

	// 7. Main backend with allowed Origin: SUCCESS (200 OK)
	reqMainAllowedOrigin := httptest.NewRequest("GET", "/api/movie/550", nil)
	reqMainAllowedOrigin.Header.Set("Origin", "https://api.kineflex.site")
	recMainAllowedOrigin := httptest.NewRecorder()
	mw.ServeHTTP(recMainAllowedOrigin, reqMainAllowedOrigin)
	if recMainAllowedOrigin.Code != http.StatusOK {
		t.Errorf("expected 200 for allowed origin, got %d", recMainAllowedOrigin.Code)
	}
	if recMainAllowedOrigin.Header().Get("Access-Control-Allow-Origin") != "https://api.kineflex.site" {
		t.Errorf("expected CORS header https://api.kineflex.site, got %s", recMainAllowedOrigin.Header().Get("Access-Control-Allow-Origin"))
	}

	// 8. Main backend with allowed Referer: SUCCESS (200 OK)
	reqMainAllowedRef := httptest.NewRequest("GET", "/api/movie/550", nil)
	reqMainAllowedRef.Header.Set("Referer", "https://api.kineflex.site/browse")
	recMainAllowedRef := httptest.NewRecorder()
	mw.ServeHTTP(recMainAllowedRef, reqMainAllowedRef)
	if recMainAllowedRef.Code != http.StatusOK {
		t.Errorf("expected 200 for allowed referer, got %d", recMainAllowedRef.Code)
	}
}
