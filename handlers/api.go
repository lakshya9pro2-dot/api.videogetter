package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"vidara-api/models"
	"vidara-api/resolver"
	"vidara-api/tiers"
)

const (
	StreamtapeDefaultURL = "https://streamtape.com/v/0A2vDYQz3wIbPQ6/"
	VidaraDefaultURL     = "https://vidara.to/v/d932127894f1"
)

// APIHandler handles HTTP requests for the multi-tier streaming API.
type APIHandler struct {
	resolver   *resolver.CentralResolver
	vidaraTier *tiers.VidaraTier
	tier3      *tiers.Tier3
	httpClient *http.Client
}

// NewAPIHandler creates a new APIHandler instance.
func NewAPIHandler(res *resolver.CentralResolver, vidaraTier *tiers.VidaraTier, tier3 *tiers.Tier3, timeout time.Duration) *APIHandler {
	return &APIHandler{
		resolver:   res,
		vidaraTier: vidaraTier,
		tier3:      tier3,
		httpClient: &http.Client{Timeout: timeout},
	}
}

// AddCORSHeaders sets standard permissive CORS headers on the response.
func AddCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, HEAD")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges")
}

// HandleHealth handles GET /health.
func (h *APIHandler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	AddCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
	})
}

// HandleMovie handles GET /api/movie/{tmdb_id}.
func (h *APIHandler) HandleMovie(w http.ResponseWriter, r *http.Request) {
	AddCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	rawID := strings.TrimPrefix(r.URL.Path, "/api/movie/")
	rawID = strings.Trim(rawID, "/")

	isM3U := strings.EqualFold(r.URL.Query().Get("format"), "m3u") ||
		strings.Contains(rawID, ".m3u") ||
		strings.Contains(rawID, "/playlist.m3u")

	cleanedID := rawID
	if idx := strings.Index(cleanedID, ".m3u"); idx != -1 {
		cleanedID = cleanedID[:idx]
	}
	cleanedID = strings.TrimSuffix(cleanedID, "/playlist")
	cleanedID = strings.Trim(cleanedID, "/")
	if idx := strings.Index(cleanedID, "&"); idx != -1 {
		cleanedID = cleanedID[:idx]
	}
	if idx := strings.Index(cleanedID, "?"); idx != -1 {
		cleanedID = cleanedID[:idx]
	}

	tmdbID, err := strconv.Atoi(cleanedID)
	if err != nil || tmdbID <= 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "invalid_parameters",
			"message": "tmdb_id must be a positive integer",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()

	if isM3U {
		// If Tier 3 is available, fetch all ShowBox M3U8 streams (strictly M3U8, no MKV/MP4)
		if h.tier3 != nil {
			title, streams, err := h.tier3.FetchAllM3U8MovieStreams(ctx, tmdbID)
			if err == nil && len(streams) > 0 {
				m3uContent := h.tier3.GenerateM3U(title, streams)
				w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
				w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%d.m3u\"", tmdbID))
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(m3uContent))
				return
			}
		}

		// Fallback: resolve movie via standard tiers and format into Extended M3U
		result, err := h.resolver.ResolveMovie(ctx, tmdbID)
		if err == nil && result != nil && result.Success && result.URL != "" {
			title := result.Title
			if title == "" {
				title = fmt.Sprintf("Movie %d", tmdbID)
			}
			ref := ""
			orig := ""
			if result.Headers != nil {
				ref = result.Headers["Referer"]
				orig = result.Headers["Origin"]
			}
			m3uContent := tiers.GenerateSingleM3U(title, result.URL, ref, orig, "")
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
			w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%d.m3u\"", tmdbID))
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(m3uContent))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"tmdb_id": tmdbID,
			"error":   "source_not_found",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	result, err := h.resolver.ResolveMovie(ctx, tmdbID)
	if err != nil || result == nil || !result.Success {
		w.WriteHeader(http.StatusNotFound)
		if result == nil {
			result = &models.ResolutionResult{
				Success: false,
				TmdbID:  tmdbID,
				Error:   "source_not_found",
			}
		}
		_ = json.NewEncoder(w).Encode(result)
		return
	}

	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "localhost:8080"
	}
	baseURL := fmt.Sprintf("%s://%s", scheme, host)

	if result.Tier == 3 {
		if result.Expiry != nil && *result.Expiry > 0 {
			result.URL = fmt.Sprintf("%s/api/movie/%d.m3u&t=%d", baseURL, tmdbID, *result.Expiry)
		} else {
			result.URL = fmt.Sprintf("%s/api/movie/%d.m3u", baseURL, tmdbID)
		}
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
}

// HandleTV handles GET /api/tv/{tmdb_id}/{season_number}/{episode_number}.
func (h *APIHandler) HandleTV(w http.ResponseWriter, r *http.Request) {
	AddCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/tv/")
	isM3U := strings.EqualFold(r.URL.Query().Get("format"), "m3u") ||
		strings.Contains(path, ".m3u") ||
		strings.Contains(path, "/playlist.m3u")

	cleanedPath := path
	if idx := strings.Index(cleanedPath, ".m3u"); idx != -1 {
		cleanedPath = cleanedPath[:idx]
	}
	cleanedPath = strings.TrimSuffix(cleanedPath, "/playlist")
	cleanedPath = strings.Trim(cleanedPath, "/")

	parts := strings.Split(cleanedPath, "/")
	if len(parts) < 3 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "invalid_parameters",
			"message": "usage: /api/tv/{tmdb_id}/{season}/{episode}",
		})
		return
	}

	part2 := parts[2]
	if idx := strings.Index(part2, "&"); idx != -1 {
		part2 = part2[:idx]
	}
	if idx := strings.Index(part2, "?"); idx != -1 {
		part2 = part2[:idx]
	}

	tmdbID, errID := strconv.Atoi(parts[0])
	season, errS := strconv.Atoi(parts[1])
	episode, errE := strconv.Atoi(part2)

	if errID != nil || errS != nil || errE != nil || tmdbID <= 0 || season <= 0 || episode <= 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "invalid_parameters",
			"message": "tmdb_id, season, and episode must be positive integers",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()

	if isM3U {
		// If Tier 3 is available, fetch all ShowBox M3U8 streams (strictly M3U8, no MKV/MP4)
		if h.tier3 != nil {
			title, streams, err := h.tier3.FetchAllM3U8TVStreams(ctx, tmdbID, season, episode)
			if err == nil && len(streams) > 0 {
				epTitle := fmt.Sprintf("%s S%02dE%02d", title, season, episode)
				m3uContent := h.tier3.GenerateM3U(epTitle, streams)
				w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
				w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%d_S%dE%d.m3u\"", tmdbID, season, episode))
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(m3uContent))
				return
			}
		}

		// Fallback: resolve TV via standard tiers and format into Extended M3U
		result, err := h.resolver.ResolveTV(ctx, tmdbID, season, episode)
		if err == nil && result != nil && result.Success && result.URL != "" {
			title := result.Title
			if title == "" {
				title = fmt.Sprintf("TV %d", tmdbID)
			}
			epTitle := fmt.Sprintf("%s S%02dE%02d", title, season, episode)
			ref := ""
			orig := ""
			if result.Headers != nil {
				ref = result.Headers["Referer"]
				orig = result.Headers["Origin"]
			}
			m3uContent := tiers.GenerateSingleM3U(epTitle, result.URL, ref, orig, "")
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
			w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%d_S%dE%d.m3u\"", tmdbID, season, episode))
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(m3uContent))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"tmdb_id": tmdbID,
			"season":  &season,
			"episode": &episode,
			"error":   "source_not_found",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	result, err := h.resolver.ResolveTV(ctx, tmdbID, season, episode)
	if err != nil || result == nil || !result.Success {
		w.WriteHeader(http.StatusNotFound)
		if result == nil {
			result = &models.ResolutionResult{
				Success: false,
				TmdbID:  tmdbID,
				Season:  &season,
				Episode: &episode,
				Error:   "source_not_found",
			}
		}
		_ = json.NewEncoder(w).Encode(result)
		return
	}

	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = "localhost:8080"
	}
	baseURL := fmt.Sprintf("%s://%s", scheme, host)

	if result.Tier == 3 {
		if result.Expiry != nil && *result.Expiry > 0 {
			result.URL = fmt.Sprintf("%s/api/tv/%d/%d/%d.m3u&t=%d", baseURL, tmdbID, season, episode, *result.Expiry)
		} else {
			result.URL = fmt.Sprintf("%s/api/tv/%d/%d/%d.m3u", baseURL, tmdbID, season, episode)
		}
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
}

// HandleResolve handles GET / POST /api/resolve (preserving app.py functionality).
func (h *APIHandler) HandleResolve(w http.ResponseWriter, r *http.Request) {
	AddCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	stParsed, vaParsed := tiers.ParseIDsFromString(r.URL.RequestURI())
	stInput := stParsed
	vaInput := vaParsed

	if r.Method == http.MethodPost {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if st, ok := body["st"].(string); ok && st != "" {
			stInput = st
		} else if play, ok := body["play"].(string); ok && play != "" {
			stInput = play
		}
		if va, ok := body["va"].(string); ok && va != "" {
			vaInput = va
		} else if playd, ok := body["playd"].(string); ok && playd != "" {
			vaInput = playd
		}
	} else {
		q := r.URL.Query()
		if st := q.Get("st"); st != "" {
			stInput = st
		}
		if va := q.Get("va"); va != "" {
			vaInput = va
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	resp := h.vidaraTier.ResolveDual(ctx, stInput, vaInput)
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleExtract handles GET / POST /api/extract (preserving app.py functionality).
func (h *APIHandler) HandleExtract(w http.ResponseWriter, r *http.Request) {
	AddCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	targetURL := r.URL.Query().Get("url")
	formatType := strings.ToLower(r.URL.Query().Get("format"))

	if r.Method == http.MethodPost && targetURL == "" {
		_ = r.ParseForm()
		targetURL = r.FormValue("url")
		if targetURL == "" {
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if u, ok := body["url"].(string); ok {
				targetURL = u
			}
		}
	}

	if targetURL == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "error",
			"message": "Missing 'url' parameter",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	result, err := h.vidaraTier.ExtractAny(ctx, targetURL)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "error",
			"message": err.Error(),
			"url":     targetURL,
		})
		return
	}

	accept := r.Header.Get("Accept")
	if formatType == "text" || strings.Contains(accept, "text/plain") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "Text: %s\nURL: %s\n", result.Text, result.URL)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
}

// HandleProxyStream handles GET / HEAD /api/proxy/stream with byte range support.
func (h *APIHandler) HandleProxyStream(w http.ResponseWriter, r *http.Request) {
	AddCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	targetURL := r.URL.Query().Get("url")
	if targetURL == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "error",
			"message": "Missing 'url' parameter",
		})
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, nil)
	if err != nil {
		http.Error(w, "Proxy error: "+err.Error(), http.StatusBadGateway)
		return
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	if rg := r.Header.Get("Range"); rg != "" {
		req.Header.Set("Range", rg)
	}

	upstream, err := h.httpClient.Do(req)
	if err != nil {
		http.Error(w, "Proxy error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer upstream.Body.Close()

	for _, k := range []string{"Content-Range", "Content-Length", "Content-Type", "Accept-Ranges"} {
		if v := upstream.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.Header().Set("Accept-Ranges", "bytes")

	w.WriteHeader(upstream.StatusCode)
	if r.Method == http.MethodHead {
		return
	}

	buf := make([]byte, 128*1024)
	_, _ = io.CopyBuffer(w, upstream.Body, buf)
}

// HandleStreamTapeDefault handles GET /api/streamtape.
func (h *APIHandler) HandleStreamTapeDefault(w http.ResponseWriter, r *http.Request) {
	AddCORSHeaders(w)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	result, err := h.vidaraTier.ExtractStreamTape(ctx, StreamtapeDefaultURL)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": err.Error()})
		return
	}

	if r.URL.Query().Get("format") == "text" || strings.Contains(r.Header.Get("Accept"), "text/plain") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "Text: %s\nURL: %s\n", result.Text, result.URL)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// HandleVidaraDefault handles GET /api/vidara.
func (h *APIHandler) HandleVidaraDefault(w http.ResponseWriter, r *http.Request) {
	AddCORSHeaders(w)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	result, err := h.vidaraTier.ExtractVidara(ctx, VidaraDefaultURL)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": err.Error()})
		return
	}

	if r.URL.Query().Get("format") == "text" || strings.Contains(r.Header.Get("Accept"), "text/plain") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "Text: %s\nURL: %s\n", result.Text, result.URL)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// HandleTextStreamTape handles GET /text/streamtape.
func (h *APIHandler) HandleTextStreamTape(w http.ResponseWriter, r *http.Request) {
	AddCORSHeaders(w)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	result, err := h.vidaraTier.ExtractStreamTape(ctx, StreamtapeDefaultURL)
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "Error: %v\n", err)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "Text: %s\nURL: %s\n", result.Text, result.URL)
}

// HandleTextVidara handles GET /text/vidara.
func (h *APIHandler) HandleTextVidara(w http.ResponseWriter, r *http.Request) {
	AddCORSHeaders(w)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	result, err := h.vidaraTier.ExtractVidara(ctx, VidaraDefaultURL)
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "Error: %v\n", err)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "Text: %s\nURL: %s\n", result.Text, result.URL)
}

// HandleMasterPlaylist handles GET /playlist.m3u8 and /api/playlist.m3u8.
func (h *APIHandler) HandleMasterPlaylist(w http.ResponseWriter, r *http.Request) {
	AddCORSHeaders(w)
	vaID := r.URL.Query().Get("va")
	if vaID == "" {
		vaID = "d932127894f1"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	vaRes, err := h.vidaraTier.ExtractVidara(ctx, vaID)
	if err == nil && vaRes != nil && strings.HasPrefix(vaRes.URL, "http") {
		req, err := http.NewRequestWithContext(ctx, "GET", vaRes.URL, nil)
		if err == nil {
			req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64)")
			resp, err := h.httpClient.Do(req)
			if err == nil && resp.StatusCode == http.StatusOK {
				defer resp.Body.Close()
				_, _ = io.Copy(w, resp.Body)
				return
			}
		}
	}

	w.Write([]byte("#EXTM3U\n#EXT-X-VERSION:6\n"))
}

// HandlePlayer serves player.html if available or simple HTML fallback.
func (h *APIHandler) HandlePlayer(w http.ResponseWriter, r *http.Request) {
	AddCORSHeaders(w)
	playerPath := tiers.FindFirstExistingFile("player.html")
	if playerPath != "" {
		http.ServeFile(w, r, playerPath)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := `<!DOCTYPE html>
<html>
<head><title>Vidara Player</title></head>
<body style="font-family:sans-serif;padding:20px;">
  <h1>Vidara 3-Tier Multi-Source Streaming API</h1>
  <p>Status: Running</p>
  <ul>
    <li><a href="/health">/health</a></li>
    <li>Movie API: <code>/api/movie/{tmdb_id}</code></li>
    <li>TV API: <code>/api/tv/{tmdb_id}/{season}/{episode}</code></li>
  </ul>
</body>
</html>`
	w.Write([]byte(html))
}

// HandleHlsJs serves hls.min.js if found.
func (h *APIHandler) HandleHlsJs(w http.ResponseWriter, r *http.Request) {
	AddCORSHeaders(w)
	jsPath := tiers.FindFirstExistingFile("hls.min.js")
	if jsPath != "" {
		w.Header().Set("Content-Type", "application/javascript")
		http.ServeFile(w, r, jsPath)
		return
	}
	w.Header().Set("Content-Type", "application/javascript")
	w.Write([]byte("// HLS not cached locally\n"))
}

// HandleM3UUniversal handles GET /playlist.m3u and /api/playlist.m3u.
func (h *APIHandler) HandleM3UUniversal(w http.ResponseWriter, r *http.Request) {
	AddCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	q := r.URL.Query()
	rawID := q.Get("tmdb_id")
	if rawID == "" {
		rawID = q.Get("id")
	}

	tmdbID, _ := strconv.Atoi(rawID)
	if tmdbID <= 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "missing or invalid 'id' or 'tmdb_id' query parameter",
		})
		return
	}

	seasonStr := q.Get("season")
	episodeStr := q.Get("episode")

	if seasonStr != "" && episodeStr != "" {
		s, _ := strconv.Atoi(seasonStr)
		e, _ := strconv.Atoi(episodeStr)
		if s > 0 && e > 0 {
			r.URL.Path = fmt.Sprintf("/api/tv/%d/%d/%d.m3u", tmdbID, s, e)
			h.HandleTV(w, r)
			return
		}
	}

	r.URL.Path = fmt.Sprintf("/api/movie/%d.m3u", tmdbID)
	h.HandleMovie(w, r)
}

// HandleReload handles GET/POST /api/reload and /reload to dynamically re-fetch and re-index dataset from remote URL.
func (h *APIHandler) HandleReload(w http.ResponseWriter, r *http.Request) {
	AddCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	if h.vidaraTier == nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "vidara tier not initialized",
		})
		return
	}

	if err := h.vidaraTier.ReloadData(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	movies, tv := h.vidaraTier.GetCounts()
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success":     true,
		"message":     "dataset successfully reloaded",
		"movies":      movies,
		"tv_episodes": tv,
	})
}
