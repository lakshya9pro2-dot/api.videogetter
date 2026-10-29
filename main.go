package main

import (
	"fmt"
	"log"
	"net/http"

	"vidara-api/handlers"
	"vidara-api/proxy"
	"vidara-api/resolver"
	"vidara-api/tiers"
)

func main() {
	cfg := LoadConfig()

	log.Printf("[*] Initializing 4-Tier Multi-Source Streaming API...")

	// 1. Tier 1 - Vidara & StreamHG
	tier1 := tiers.NewVidaraTier(cfg.VidaraURL, cfg.StreamTapeURL, cfg.RequestTimeout)
	tier1.SetStreamHGBase(cfg.StreamHGBaseURL)
	if err := tier1.LoadData(cfg.VidaraRemoteURL, cfg.DataFilePath, "data/vidara.json", "data.json"); err != nil {
		log.Printf("[!] Warning loading Vidara data: %v", err)
	}

	// 2. Tier 2 - Extractor API
	tier2 := tiers.NewExtractorTier(cfg.AppURLWorker, cfg.VidFastBaseURL, cfg.RequestTimeout)
	if cfg.VidFastTimeoutSeconds > 0 {
		tier2.SetTimeoutSeconds(cfg.VidFastTimeoutSeconds)
	}

	// 3. Tier 3 - CineTV & Vidrift
	tier3 := tiers.NewVidriftTier(cfg.VidriftBaseURL, cfg.TMDBKey, cfg.RequestTimeout)
	if cfg.CineTVBaseURL != "" {
		tier3.SetCineTVBaseURL(cfg.CineTVBaseURL)
		log.Printf("[*] Tier 3 CineTV enabled with base URL: %s", cfg.CineTVBaseURL)
	}

	// 4. Tier 4 - ShowBox (FilmU)
	tier4 := tiers.NewTier4(cfg.ShowBoxBaseURL, cfg.ShowBoxAPIKey, cfg.FebboxToken, cfg.TMDBKey, cfg.RequestTimeout)

	// Central Fallback Resolver (Tier 1 -> Tier 2 -> Tier 3 -> Tier 4)
	centralResolver := resolver.NewCentralResolver(tier1, tier2, tier3, tier4)

	// API Handlers
	apiHandler := handlers.NewAPIHandler(centralResolver, tier1, tier4, cfg.RequestTimeout)

	// Cloudflare Worker Playback Proxy Client (Part B)
	if len(cfg.ProxyWorkerURLs) > 0 {
		workerClient := proxy.NewWorkerClient(cfg.ProxyWorkerURLs, cfg.ProxyWorkerSecret, cfg.ProxyWorkerTimeout)
		apiHandler.SetWorkerClient(workerClient, cfg.ProxyFallbackDirect)
		log.Printf("[*] Cloudflare Worker HLS Proxy enabled with %d worker(s): %v (fallback_direct: %v)",
			len(cfg.ProxyWorkerURLs), cfg.ProxyWorkerURLs, cfg.ProxyFallbackDirect)
	} else {
		log.Printf("[*] Cloudflare Worker HLS Proxy inactive (no PROXY_WORKER_URLS configured)")
	}

	mux := http.NewServeMux()

	// Core 3-Tier endpoints
	mux.HandleFunc("/health", apiHandler.HandleHealth)
	mux.HandleFunc("/api/movie/", apiHandler.HandleMovie)
	mux.HandleFunc("/api/tv/", apiHandler.HandleTV)
	mux.HandleFunc("/playlist.m3u", apiHandler.HandleM3UUniversal)
	mux.HandleFunc("/api/playlist.m3u", apiHandler.HandleM3UUniversal)
	mux.HandleFunc("/api/reload", apiHandler.HandleReload)
	mux.HandleFunc("/reload", apiHandler.HandleReload)

	// Preserved Vidara app.py endpoints
	mux.HandleFunc("/api/resolve", apiHandler.HandleResolve)
	mux.HandleFunc("/api/resolve/", apiHandler.HandleResolve)
	mux.HandleFunc("/api/extract", apiHandler.HandleExtract)
	mux.HandleFunc("/api/proxy/stream", apiHandler.HandleProxyStream)
	mux.HandleFunc("/api/vidara", apiHandler.HandleVidaraDefault)
	mux.HandleFunc("/api/streamtape", apiHandler.HandleStreamTapeDefault)
	mux.HandleFunc("/text/vidara", apiHandler.HandleTextVidara)
	mux.HandleFunc("/text/streamtape", apiHandler.HandleTextStreamTape)
	mux.HandleFunc("/playlist.m3u8", apiHandler.HandleMasterPlaylist)
	mux.HandleFunc("/api/playlist.m3u8", apiHandler.HandleMasterPlaylist)
	mux.HandleFunc("/player.html", apiHandler.HandlePlayer)
	mux.HandleFunc("/player.html/", apiHandler.HandlePlayer)
	mux.HandleFunc("/player", apiHandler.HandlePlayer)
	mux.HandleFunc("/player/", apiHandler.HandlePlayer)
	mux.HandleFunc("/hls.min.js", apiHandler.HandleHlsJs)
	mux.HandleFunc("/", apiHandler.HandlePlayer)

	handler := handlers.OriginFilterMiddleware(mux, cfg.AllowedOrigin, cfg.RedirectURL, cfg.APIKey)

	addr := fmt.Sprintf("0.0.0.0:%s", cfg.Port)
	log.Printf("[*] Server listening on http://%s", addr)
	if err := http.ListenAndServe(":"+cfg.Port, handler); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
