package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// ============================================================================
// EASY CONFIGURATION: EDIT THIS URL TO CHANGE THE JSON DATASET LOCATION
// ============================================================================
const DefaultVidaraJSONURL = "https://www.jsonkeeper.com/b/FWSAK"

// AllowedOrigin is the strictly allowed Origin/Referer for backend API access.
const DefaultAllowedOrigin = "https://api.kineflex.site"

// RedirectURL is where unauthorized users/browsers are redirected.
const DefaultRedirectURL = "https://api.kineflex.site/"

// DefaultBackendAPIKey is the required secret key sent by api.kineflex.site.
const DefaultBackendAPIKey = "lakshya9310081539@"

// DefaultProxyWorkerURL is the primary Cloudflare Worker endpoint for HLS playback proxying.
const DefaultProxyWorkerURL = "https://turbo-octo-succotash.kineflex-netflex.workers.dev"

// DefaultCineTVBaseURL is the default base URL for the CineTV (Netlify) resolver in Tier 3.
const DefaultCineTVBaseURL = "https://cinetvtest.netlify.app"
// ============================================================================

// Config holds runtime configuration settings.
type Config struct {
	Port            string
	VidaraRemoteURL string // Remote URL for loading Vidara JSON dataset
	AllowedOrigin   string // Allowed Origin for backend API access
	RedirectURL     string // Redirect URL for unauthorized backend visits
	APIKey          string // Required API key sent by api.kineflex.site
	AppURLWorker    string
	ExtractorURL    string
	VidFastBaseURL  string
	VidaraURL       string
	StreamTapeURL   string
	StreamHGBaseURL string
	VidriftBaseURL  string // Base URL for Vidrift embed host (Tier 3)
	CineTVBaseURL   string // Base URL for CineTV (Netlify) resolver (Tier 3)
	Tier3BaseURL    string
	Tier4BaseURL    string // Base URL for ShowBox/FilmU (Tier 4)
	ShowBoxBaseURL  string
	ShowBoxAPIKey   string
	FebboxToken     string
	TMDBKey         string
	DataFilePath    string
	RequestTimeout  time.Duration
	VidFastTimeoutSeconds int
	ProxyWorkerURLs       []string      // Configured Cloudflare Worker endpoints for HLS playback proxying
	ProxyWorkerSecret     string        // Shared secret sent to Cloudflare Workers (X-Worker-Key)
	ProxyWorkerTimeout    time.Duration // Timeout per worker attempt
	ProxyFallbackDirect   bool          // Fallback to direct stream URL if all workers fail
}

// LoadConfig initializes Config from environment variables or sensible defaults.
func LoadConfig() *Config {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	appURLWorker := "https://pinggy-registry.kineflex-netflex.workers.dev/api/app"
	if envWorker := os.Getenv("APP_URL_WORKER"); envWorker != "" {
		appURLWorker = envWorker
	}

	extractorURL := os.Getenv("EXTRACTOR_URL")
	if extractorURL == "" {
		extractorURL = "http://192.168.1.2:8080"
	}

	vidfastBaseURL := os.Getenv("VIDFAST_BASE_URL")
	if vidfastBaseURL == "" {
		vidfastBaseURL = "https://vidfast.vc"
	}

	vidaraURL := os.Getenv("VIDARA_BASE_URL")
	if vidaraURL == "" {
		vidaraURL = "https://vidara.to"
	}

	streamTapeURL := os.Getenv("STREAMTAPE_BASE_URL")
	if streamTapeURL == "" {
		streamTapeURL = "https://streamtape.com"
	}

	streamHGBaseURL := os.Getenv("STREAMHG_BASE_URL")
	if streamHGBaseURL == "" {
		streamHGBaseURL = os.Getenv("STREAMHG_URL")
	}
	if streamHGBaseURL == "" {
		streamHGBaseURL = "https://hanerix.com"
	}

	vidriftBaseURL := os.Getenv("VIDRIFT_BASE_URL")
	if vidriftBaseURL == "" {
		vidriftBaseURL = "https://embed.vidrift.in"
	}

	cinetvBaseURL := os.Getenv("CINETV_BASE_URL")
	if cinetvBaseURL == "" {
		cinetvBaseURL = DefaultCineTVBaseURL
	}

	tier3BaseURL := vidriftBaseURL

	tier4BaseURL := os.Getenv("TIER4_BASE_URL")
	if tier4BaseURL == "" {
		tier4BaseURL = os.Getenv("FILMIN_BASE_URL")
	}

	showBoxBaseURL := os.Getenv("SHOWBOX_BASE_URL")
	if showBoxBaseURL == "" {
		showBoxBaseURL = tier4BaseURL
	}
	if showBoxBaseURL == "" {
		showBoxBaseURL = "https://box.filmu.in"
	}

	showBoxAPIKey := os.Getenv("SHOWBOX_API_KEY")
	if showBoxAPIKey == "" {
		showBoxAPIKey = "09eb429913afb6b1cc90f23746f41fb3279aed77726c625c40672b81444c0bac"
	}

	febboxToken := os.Getenv("FEBBOX_TOKEN")
	if febboxToken == "" {
		febboxToken = "eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzI1NiJ9.eyJpYXQiOjE3OTE3MDE2MTksIm5iZiI6MTc5MTcwMTYxOSwiZXhwIjoxODIyODA1NjM5LCJkYXRhIjp7InVpZCI6MjM1NjE4OCwidG9rZW4iOiI4NDgxZDZmZDU1YWFjMzE0NmM4YmVhYjc0MDk3ZjIyMiJ9fQ.3XnZ66gABz4BnBzwuF1Pbpt-EqNX3y3UQ41JKB50gl4"
	}

	tmdbKey := os.Getenv("TMDB_KEY")
	if tmdbKey == "" {
		tmdbKey = "e6333b32409e02a4a6eba6fb7ff866bb"
	}

	dataFilePath := os.Getenv("DATA_FILE")
	if dataFilePath == "" {
		dataFilePath = "data/vidara.json"
	}

	timeoutSec := 10
	if secStr := os.Getenv("REQUEST_TIMEOUT_SECONDS"); secStr != "" {
		if s, err := strconv.Atoi(secStr); err == nil && s > 0 {
			timeoutSec = s
		}
	}

	vidfastTimeoutSec := 10
	if secStr := os.Getenv("VIDFAST_TIMEOUT_SECONDS"); secStr != "" {
		if s, err := strconv.Atoi(secStr); err == nil && s > 0 {
			vidfastTimeoutSec = s
		}
	}

	vidaraRemoteURL := os.Getenv("VIDARA_JSON_URL")
	if vidaraRemoteURL == "" {
		vidaraRemoteURL = os.Getenv("DATA_URL")
	}
	if vidaraRemoteURL == "" {
		vidaraRemoteURL = DefaultVidaraJSONURL
	}

	allowedOrigin := os.Getenv("ALLOWED_ORIGIN")
	if allowedOrigin == "" {
		allowedOrigin = DefaultAllowedOrigin
	}

	redirectURL := os.Getenv("REDIRECT_URL")
	if redirectURL == "" {
		redirectURL = DefaultRedirectURL
	}

	apiKey := os.Getenv("API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("BACKEND_API_KEY")
	}
	if apiKey == "" {
		apiKey = DefaultBackendAPIKey
	}

	var proxyWorkerURLs []string
	if envWorkerURLs := os.Getenv("PROXY_WORKER_URLS"); envWorkerURLs != "" {
		for _, u := range strings.Split(envWorkerURLs, ",") {
			if trimmed := strings.TrimSpace(u); trimmed != "" {
				proxyWorkerURLs = append(proxyWorkerURLs, strings.TrimRight(trimmed, "/"))
			}
		}
	}
	if len(proxyWorkerURLs) == 0 {
		for _, envKey := range []string{"PROXY_WORKER_URL_1", "PROXY_WORKER_URL_2", "PROXY_WORKER_URL_3"} {
			if u := strings.TrimSpace(os.Getenv(envKey)); u != "" {
				proxyWorkerURLs = append(proxyWorkerURLs, strings.TrimRight(u, "/"))
			}
		}
	}
	if len(proxyWorkerURLs) == 0 {
		proxyWorkerURLs = []string{DefaultProxyWorkerURL}
	}

	proxyWorkerSecret := os.Getenv("PROXY_WORKER_SECRET")
	if proxyWorkerSecret == "" {
		proxyWorkerSecret = os.Getenv("WORKER_SECRET")
	}

	proxyWorkerTimeoutSec := 10
	if secStr := os.Getenv("PROXY_WORKER_TIMEOUT_SECONDS"); secStr != "" {
		if s, err := strconv.Atoi(secStr); err == nil && s > 0 {
			proxyWorkerTimeoutSec = s
		}
	}

	proxyFallbackDirect := false
	if fbStr := os.Getenv("PROXY_FALLBACK_DIRECT"); fbStr != "" {
		proxyFallbackDirect = strings.EqualFold(fbStr, "true") || fbStr == "1"
	}

	return &Config{
		Port:            port,
		VidaraRemoteURL: vidaraRemoteURL,
		AllowedOrigin:   allowedOrigin,
		RedirectURL:     redirectURL,
		APIKey:          apiKey,
		AppURLWorker:    appURLWorker,
		ExtractorURL:    extractorURL,
		VidFastBaseURL:  vidfastBaseURL,
		VidaraURL:       vidaraURL,
		StreamTapeURL:   streamTapeURL,
		StreamHGBaseURL: streamHGBaseURL,
		VidriftBaseURL:  vidriftBaseURL,
		CineTVBaseURL:   cinetvBaseURL,
		Tier3BaseURL:    tier3BaseURL,
		Tier4BaseURL:    tier4BaseURL,
		ShowBoxBaseURL:  showBoxBaseURL,
		ShowBoxAPIKey:   showBoxAPIKey,
		FebboxToken:     febboxToken,
		TMDBKey:         tmdbKey,
		DataFilePath:    dataFilePath,
		RequestTimeout:  time.Duration(timeoutSec) * time.Second,
		VidFastTimeoutSeconds: vidfastTimeoutSec,
		ProxyWorkerURLs:       proxyWorkerURLs,
		ProxyWorkerSecret:     proxyWorkerSecret,
		ProxyWorkerTimeout:    time.Duration(proxyWorkerTimeoutSec) * time.Second,
		ProxyFallbackDirect:   proxyFallbackDirect,
	}
}
