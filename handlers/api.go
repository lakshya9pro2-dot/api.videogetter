package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"vidara-api/models"
	"vidara-api/proxy"
	"vidara-api/resolver"
	"vidara-api/tiers"
)

const (
	StreamtapeDefaultURL = "https://streamtape.com/v/0A2vDYQz3wIbPQ6/"
	VidaraDefaultURL     = "https://mxzhxco8dft.com/v/d932127894f1"
)

// APIHandler handles HTTP requests for the multi-tier streaming API.
type APIHandler struct {
	resolver            *resolver.CentralResolver
	vidaraTier          *tiers.VidaraTier
	tier3               *tiers.Tier3
	workerClient        *proxy.WorkerClient
	proxyFallbackDirect bool
	webWorkerURL        string
	httpClient          *http.Client
	cache               *ResolutionCache
	sf                  *SingleflightGroup
	cacheTTL            time.Duration
}

// NewAPIHandler creates a new APIHandler instance.
func NewAPIHandler(res *resolver.CentralResolver, vidaraTier *tiers.VidaraTier, tier3 *tiers.Tier3, timeout time.Duration) *APIHandler {
	webWorkerURL := "https://proxyservr.kineflex-netflex.workers.dev"
	if vidaraTier != nil && vidaraTier.GetHubCloudProxyURL() != "" {
		webWorkerURL = vidaraTier.GetHubCloudProxyURL()
	}
	return &APIHandler{
		resolver:     res,
		vidaraTier:   vidaraTier,
		tier3:        tier3,
		webWorkerURL: webWorkerURL,
		httpClient:   &http.Client{Timeout: timeout},
		cache:        NewResolutionCache(),
		sf:           NewSingleflightGroup(),
		cacheTTL:     30 * time.Minute,
	}
}

// SetWebWorkerURL sets the worker URL for non-Streamtape streams in ?web mode (default: https://proxyservr.kineflex-netflex.workers.dev).
func (h *APIHandler) SetWebWorkerURL(urlStr string) {
	if urlStr != "" {
		h.webWorkerURL = strings.TrimRight(strings.TrimSpace(urlStr), "/")
	}
}

// GetWebWorkerURL returns the active web worker URL for non-Streamtape sources.
func (h *APIHandler) GetWebWorkerURL() string {
	if h.webWorkerURL != "" {
		return h.webWorkerURL
	}
	if h.vidaraTier != nil && h.vidaraTier.GetHubCloudProxyURL() != "" {
		return h.vidaraTier.GetHubCloudProxyURL()
	}
	return "https://proxyservr.kineflex-netflex.workers.dev"
}

// ClearCache flushes the in-memory resolution cache.
func (h *APIHandler) ClearCache() {
	if h.cache != nil {
		h.cache.Clear()
	}
}

// SetWorkerClient configures the Netlify Worker proxy client and fallback policy (used exclusively for Streamtape).
func (h *APIHandler) SetWorkerClient(client *proxy.WorkerClient, fallbackDirect bool) {
	h.workerClient = client
	h.proxyFallbackDirect = fallbackDirect
}

// IsWebRequest returns true if the request query parameter contains ?web, ?web=1, ?web=true, etc.
func IsWebRequest(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	if r.URL.Query().Has("web") {
		val := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("web")))
		if val == "false" || val == "0" || val == "no" {
			return false
		}
		return true
	}
	rawQ := strings.ToLower(r.URL.RawQuery)
	if strings.Contains(rawQ, "web") {
		return true
	}
	rawURI := strings.ToLower(r.URL.RequestURI())
	if strings.Contains(rawURI, "?web") || strings.Contains(rawURI, "&web") {
		return true
	}
	return false
}

// proxyViaWorkerHide proxies any stream URL via the proxyservr worker (/api/hide or /proxy?url=).
func proxyViaWorkerHide(ctx context.Context, client *http.Client, proxyBase, rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("empty raw URL")
	}
	if proxyBase == "" {
		proxyBase = "https://proxyservr.kineflex-netflex.workers.dev"
	}
	proxyBase = strings.TrimRight(proxyBase, "/")
	if strings.HasPrefix(rawURL, proxyBase) {
		return rawURL, nil
	}

	hideEndpoint := fmt.Sprintf("%s/api/hide", proxyBase)
	reqBody, err := json.Marshal(map[string]string{"url": rawURL})
	if err != nil {
		return fmt.Sprintf("%s/proxy?url=%s", proxyBase, url.QueryEscape(rawURL)), nil
	}

	reqCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, hideEndpoint, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Sprintf("%s/proxy?url=%s", proxyBase, url.QueryEscape(rawURL)), nil
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120.0.0.0")

	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Sprintf("%s/proxy?url=%s", proxyBase, url.QueryEscape(rawURL)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var hideResp struct {
			StreamURL string `json:"streamUrl"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&hideResp); err == nil && hideResp.StreamURL != "" {
			streamPath := hideResp.StreamURL
			if !strings.HasPrefix(streamPath, "/") {
				streamPath = "/" + streamPath
			}
			return proxyBase + streamPath, nil
		}
	}

	return fmt.Sprintf("%s/proxy?url=%s", proxyBase, url.QueryEscape(rawURL)), nil
}

// proxyResolvedResult rewrites a resolved media stream according to specific proxy rules:
// - Direct MP4: Always plays WITHOUT proxy in BOTH normal and web mode.
// - Streamtape (st) & StreamHG (sh/sg): Strictly proxied via worker (workerClient) in BOTH normal and web mode.
// - Normal mode (!isWeb): ONLY Streamtape and StreamHG are proxied. All other sources (Vidara, HubCloud, Tiers 2-4) remain DIRECT.
// - Web mode (isWeb == true, ?web): Streamtape & StreamHG use workerClient; all other sources (Vidara, HubCloud, Tiers 2-4) use https://proxyservr.kineflex-netflex.workers.dev.
func (h *APIHandler) proxyResolvedResult(ctx context.Context, result *models.ResolutionResult, videoID string, mediaType string, isWeb bool) error {
	if result == nil || !result.Success || strings.TrimSpace(result.URL) == "" {
		return nil
	}

	// 1. Explicit Direct sources (source == "direct"): Always play WITHOUT proxy in BOTH normal and web mode!
	// (Note: CineTV and other tiered sources are NOT skipped here so they get proxied in ?web mode)
	if result.Source == "direct" {
		log.Printf("[proxy] Direct source detected for %s -> playing DIRECT WITHOUT PROXY: %s", videoID, result.URL)
		return nil
	}

	isStreamTape := result.Source == "streamtape" || strings.EqualFold(result.Source, "st")
	isStreamHG := result.Source == "streamhg" || strings.EqualFold(result.Source, "sh") || strings.EqualFold(result.Source, "sg")

	// 2. Streamtape (st) and StreamHG (sh/sg): Strictly use workerClient in BOTH normal and web mode
	if isStreamTape || isStreamHG {
		// Skip if URL is already a proxied worker URL
		if h.workerClient != nil {
			for _, wURL := range h.workerClient.GetWorkerURLs() {
				if strings.HasPrefix(result.URL, wURL) {
					return nil
				}
			}
		}

		if h.workerClient == nil || !h.workerClient.HasWorkers() {
			return nil
		}

		provider := "streamtape"
		if isStreamHG {
			provider = "streamhg"
		}

		var sessResp *proxy.SessionResponse
		var err error

		// Try in-worker extraction if provider ID is available
		var providerID string
		if isStreamTape && result.Play != nil && result.Play.ST != "" {
			providerID = result.Play.ST
		} else if isStreamHG && result.Play != nil && result.Play.SH != "" {
			providerID = result.Play.SH
		}

		if providerID != "" {
			sessResp, err = h.workerClient.ExtractSession(ctx, proxy.ExtractSessionRequest{
				Provider: provider,
				ID:       providerID,
				VideoID:  videoID,
				Type:     mediaType,
			})
		}

		// If in-worker extraction was not performed or failed, create session from URL
		if sessResp == nil || sessResp.PlaybackURL == "" {
			sessResp, err = h.workerClient.CreateSession(ctx, proxy.SessionRequest{
				URL:     result.URL,
				Headers: result.Headers,
				VideoID: videoID,
				Type:    mediaType,
			})
		}

		if err != nil {
			if h.proxyFallbackDirect {
				log.Printf("[handlers] %s proxy worker failed for %s (%s), falling back to direct URL: %v", provider, videoID, result.URL, err)
				return nil
			}
			return fmt.Errorf("failed to register %s playback proxy session: %w", provider, err)
		}

		playbackURL := sessResp.PlaybackURL
		if isStreamTape && strings.HasSuffix(playbackURL, "/master.m3u8") {
			playbackURL = strings.TrimSuffix(playbackURL, "/master.m3u8") + "/video.mp4"
		}

		result.URL = playbackURL
		result.Direct = false
		log.Printf("[proxy] Successfully proxied %s for %s via Netlify Worker: %s", provider, videoID, playbackURL)

		if result.Headers != nil {
			delete(result.Headers, "Origin")
			delete(result.Headers, "Referer")
			delete(result.Headers, "origin")
			delete(result.Headers, "referer")
		}
		if result.Play != nil {
			result.Play.URL = playbackURL
		}
		return nil
	}

	// 3. In normal mode (!isWeb), ONLY Streamtape and StreamHG are proxied.
	// All other sources (Vidara, HubCloud, Tiers 2-4) remain DIRECT.
	if !isWeb {
		log.Printf("[proxy] Normal mode (non-web) for %s (source: %s) -> serving direct stream: %s", videoID, result.Source, result.URL)
		return nil
	}

	// 4. In ?web mode for ALL OTHER sources (Vidara, HubCloud, Tiers 2-4):
	// Strictly use https://proxyservr.kineflex-netflex.workers.dev
	webWorkerBase := h.GetWebWorkerURL()
	if strings.HasPrefix(result.URL, webWorkerBase) {
		return nil
	}

	var proxiedURL string
	var pErr error

	if h.vidaraTier != nil {
		proxiedURL, pErr = h.vidaraTier.ProxyHubCloudStream(ctx, result.URL)
	} else {
		proxiedURL, pErr = proxyViaWorkerHide(ctx, h.httpClient, webWorkerBase, result.URL)
	}

	if pErr == nil && proxiedURL != "" {
		result.URL = proxiedURL
		result.Direct = false
		log.Printf("[proxy] ?web mode for %s (source: %s) -> successfully proxied via proxyservr: %s", videoID, result.Source, proxiedURL)
		if result.Headers != nil {
			delete(result.Headers, "Origin")
			delete(result.Headers, "Referer")
			delete(result.Headers, "origin")
			delete(result.Headers, "referer")
		}
		if result.Play != nil {
			result.Play.URL = proxiedURL
		}
		return nil
	} else if pErr != nil {
		log.Printf("[handlers] proxyservr proxy failed for %s (%s): %v, keeping direct URL", videoID, result.URL, pErr)
	}

	return nil
}

// AddCORSHeaders sets CORS headers, preserving any Origin already set by middleware.
func AddCORSHeaders(w http.ResponseWriter) {
	if w.Header().Get("Access-Control-Allow-Origin") == "" {
		w.Header().Set("Access-Control-Allow-Origin", "https://api.kineflex.site")
	}
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
	isWeb := IsWebRequest(r)

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

	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
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
			_ = h.proxyResolvedResult(ctx, result, strconv.Itoa(tmdbID), "movie", isWeb)
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

	cacheKey := fmt.Sprintf("movie:%d:web=%t", tmdbID, isWeb)
	noCache := r.URL.Query().Has("refresh") || r.URL.Query().Has("no_cache")

	if !noCache && !isM3U {
		if cachedResult, ok := h.cache.Get(cacheKey); ok && cachedResult != nil {
			log.Printf("[cache hit] movie %d (web=%t)", tmdbID, isWeb)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Cache", "HIT")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(cachedResult)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	resVal, err := h.sf.Do(cacheKey, func() (*models.ResolutionResult, error) {
		res, rErr := h.resolver.ResolveMovie(ctx, tmdbID)
		if rErr != nil || res == nil || !res.Success {
			return res, rErr
		}
		if pErr := h.proxyResolvedResult(ctx, res, strconv.Itoa(tmdbID), "movie", isWeb); pErr != nil {
			return nil, pErr
		}
		return res, nil
	})

	if err != nil || resVal == nil || !resVal.Success {
		if err != nil && (resVal == nil || !resVal.Success) {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"tmdb_id": tmdbID,
				"error":   "proxy_workers_failed",
				"message": err.Error(),
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
		if resVal == nil {
			resVal = &models.ResolutionResult{
				Success: false,
				TmdbID:  tmdbID,
				Error:   "source_not_found",
			}
		}
		_ = json.NewEncoder(w).Encode(resVal)
		return
	}

	if !noCache {
		h.cache.Set(cacheKey, resVal, h.cacheTTL)
	}

	w.Header().Set("X-Cache", "MISS")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resVal)
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
	isWeb := IsWebRequest(r)

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

	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
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
			_ = h.proxyResolvedResult(ctx, result, fmt.Sprintf("%d_S%dE%d", tmdbID, season, episode), "tv", isWeb)
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

	cacheKey := fmt.Sprintf("tv:%d:%d:%d:web=%t", tmdbID, season, episode, isWeb)
	noCache := r.URL.Query().Has("refresh") || r.URL.Query().Has("no_cache")

	if !noCache && !isM3U {
		if cachedResult, ok := h.cache.Get(cacheKey); ok && cachedResult != nil {
			log.Printf("[cache hit] TV %d S%dE%d (web=%t)", tmdbID, season, episode, isWeb)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Cache", "HIT")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(cachedResult)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	resVal, err := h.sf.Do(cacheKey, func() (*models.ResolutionResult, error) {
		res, rErr := h.resolver.ResolveTV(ctx, tmdbID, season, episode)
		if rErr != nil || res == nil || !res.Success {
			return res, rErr
		}
		videoID := fmt.Sprintf("%d_S%dE%d", tmdbID, season, episode)
		if pErr := h.proxyResolvedResult(ctx, res, videoID, "tv", isWeb); pErr != nil {
			return nil, pErr
		}
		return res, nil
	})

	if err != nil || resVal == nil || !resVal.Success {
		if err != nil && (resVal == nil || !resVal.Success) {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"tmdb_id": tmdbID,
				"season":  season,
				"episode": episode,
				"error":   "proxy_workers_failed",
				"message": err.Error(),
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
		if resVal == nil {
			resVal = &models.ResolutionResult{
				Success: false,
				TmdbID:  tmdbID,
				Season:  &season,
				Episode: &episode,
				Error:   "source_not_found",
			}
		}
		_ = json.NewEncoder(w).Encode(resVal)
		return
	}

	if !noCache {
		h.cache.Set(cacheKey, resVal, h.cacheTTL)
	}

	w.Header().Set("X-Cache", "MISS")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resVal)
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

	targetURL := r.URL.Query().Get("url")
	if targetURL == "" {
		targetURL = r.URL.Query().Get("id")
	}
	if targetURL == "" {
		targetURL = StreamtapeDefaultURL
	}

	result, err := h.vidaraTier.ExtractStreamTape(ctx, targetURL)
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

	targetURL := r.URL.Query().Get("url")
	if targetURL == "" {
		targetURL = r.URL.Query().Get("id")
	}
	if targetURL == "" {
		targetURL = VidaraDefaultURL
	}

	result, err := h.vidaraTier.ExtractVidara(ctx, targetURL)
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
	respData := map[string]interface{}{
		"success":     true,
		"message":     "dataset successfully reloaded",
		"movies":      movies,
		"tv_episodes": tv,
	}
	if hubBase := h.vidaraTier.GetHubCloudBaseURL(); hubBase != "" {
		respData["hubcloud_domain"] = hubBase
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(respData)
}
