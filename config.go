package main

import (
	"os"
	"strconv"
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
// ============================================================================

// Config holds runtime configuration settings.
type Config struct {
	Port            string
	VidaraRemoteURL string // Remote URL for loading Vidara JSON dataset
	AllowedOrigin   string // Allowed Origin for backend API access
	RedirectURL     string // Redirect URL for unauthorized backend visits
	AppURLWorker    string
	ExtractorURL    string
	VidFastBaseURL  string
	VidaraURL       string
	StreamTapeURL   string
	Tier3BaseURL    string
	ShowBoxBaseURL  string
	ShowBoxAPIKey   string
	FebboxToken     string
	TMDBKey         string
	DataFilePath    string
	RequestTimeout  time.Duration
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

	tier3BaseURL := os.Getenv("TIER3_BASE_URL")
	if tier3BaseURL == "" {
		tier3BaseURL = os.Getenv("FILMIN_BASE_URL")
	}

	showBoxBaseURL := os.Getenv("SHOWBOX_BASE_URL")
	if showBoxBaseURL == "" {
		showBoxBaseURL = tier3BaseURL
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

	return &Config{
		Port:            port,
		VidaraRemoteURL: vidaraRemoteURL,
		AllowedOrigin:   allowedOrigin,
		RedirectURL:     redirectURL,
		AppURLWorker:    appURLWorker,
		ExtractorURL:    extractorURL,
		VidFastBaseURL:  vidfastBaseURL,
		VidaraURL:       vidaraURL,
		StreamTapeURL:   streamTapeURL,
		Tier3BaseURL:    tier3BaseURL,
		ShowBoxBaseURL:  showBoxBaseURL,
		ShowBoxAPIKey:   showBoxAPIKey,
		FebboxToken:     febboxToken,
		TMDBKey:         tmdbKey,
		DataFilePath:    dataFilePath,
		RequestTimeout:  time.Duration(timeoutSec) * time.Second,
	}
}
