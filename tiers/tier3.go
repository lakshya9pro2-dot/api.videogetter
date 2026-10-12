package tiers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"vidara-api/models"
)

const (
	DefaultShowBoxBaseURL = "https://box.filmu.in"
	DefaultShowBoxAPIKey  = "09eb429913afb6b1cc90f23746f41fb3279aed77726c625c40672b81444c0bac"
	DefaultFebboxToken    = "eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzI1NiJ9.eyJpYXQiOjE3OTE3MDE2MTksIm5iZiI6MTc5MTcwMTYxOSwiZXhwIjoxODIyODA1NjM5LCJkYXRhIjp7InVpZCI6MjM1NjE4OCwidG9rZW4iOiI4NDgxZDZmZDU1YWFjMzE0NmM4YmVhYjc0MDk3ZjIyMiJ9fQ.3XnZ66gABz4BnBzwuF1Pbpt-EqNX3y3UQ41JKB50gl4"
	DefaultFebboxOrigin   = "https://www.febbox.com"
	DefaultFebboxReferer  = "https://www.febbox.com/"
	DefaultUserAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

var reSize = regexp.MustCompile(`(?i)\[([\d.]+)\s*(GB|MB)\]`)

// TMDBDetails represents metadata resolved from TMDB API.
type TMDBDetails struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Year  string `json:"year"`
	Type  string `json:"type"`
}

// ShowBoxSource models an individual source entry from box.filmu.in.
type ShowBoxSource struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Quality string `json:"quality"`
	Type    string `json:"type"`
}

// ShowBoxResponse models the API response from box.filmu.in.
type ShowBoxResponse struct {
	Name    string          `json:"name"`
	Sources []ShowBoxSource `json:"sources"`
	Cached  bool            `json:"cached"`
}

// StreamOption represents a parsed and validated HLS stream option.
type StreamOption struct {
	Name        string
	Quality     string
	Type        string
	StreamURL   string
	ProxyURL    string
	Referer     string
	Origin      string
	UserAgent   string
	SizeGB      float64
	QualityRank int
	Expiry      int64
}

// Tier3 represents the FilmU / ShowBox streaming scraper for Tier 3.
type Tier3 struct {
	baseURL     string
	apiKey      string
	febboxToken string
	tmdbKey     string
	client      *http.Client
}

// NewTier3 initializes a new Tier 3 resolver instance.
func NewTier3(baseURL, apiKey, febboxToken, tmdbKey string, timeout time.Duration) *Tier3 {
	if baseURL == "" {
		baseURL = DefaultShowBoxBaseURL
	}
	if apiKey == "" {
		apiKey = DefaultShowBoxAPIKey
	}
	if febboxToken == "" {
		febboxToken = DefaultFebboxToken
	}
	if tmdbKey == "" {
		tmdbKey = "e6333b32409e02a4a6eba6fb7ff866bb"
	}

	return &Tier3{
		baseURL:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:      apiKey,
		febboxToken: febboxToken,
		tmdbKey:     tmdbKey,
		client:      &http.Client{Timeout: timeout},
	}
}

// ParseSizeInGB extracts file size from name string (e.g. "[8.09 GB]" or "[496 MB]").
func ParseSizeInGB(name string) float64 {
	m := reSize.FindStringSubmatch(name)
	if len(m) < 3 {
		return 0.0
	}
	val, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0.0
	}
	if strings.EqualFold(m[2], "GB") {
		return val
	}
	return val / 1024.0
}

// GetQualityRank assigns a sorting weight based on resolution.
func GetQualityRank(quality string) int {
	q := strings.ToLower(quality)
	if strings.Contains(q, "4k") || strings.Contains(q, "2160p") {
		return 4
	}
	if strings.Contains(q, "1080p") {
		return 3
	}
	if strings.Contains(q, "720p") {
		return 2
	}
	if strings.Contains(q, "360p") {
		return 1
	}
	return 0
}

// IsM3U8Stream filters strictly for M3U8 streams and excludes MKV, MP4, and video containers.
func IsM3U8Stream(s ShowBoxSource) bool {
	stype := strings.ToLower(strings.TrimSpace(s.Type))
	rawURL := strings.ToLower(s.URL)

	// Exclude video container types explicitly (MKV, MP4)
	if stype == "video" || strings.HasSuffix(rawURL, ".mkv") || strings.HasSuffix(rawURL, ".mp4") {
		return false
	}

	// Must be m3u8
	return stype == "m3u8" || strings.Contains(rawURL, ".m3u8")
}

// ExtractExpiry parses the unix timestamp expiry from a stream URL (parameter "t").
func ExtractExpiry(rawURL string) int64 {
	if u, err := url.Parse(rawURL); err == nil {
		if tStr := u.Query().Get("t"); tStr != "" {
			if val, err := strconv.ParseInt(tStr, 10, 64); err == nil {
				return val
			}
		}
	}
	return 0
}

// ParseSourceStream parses a ShowBox source and extracts the direct stream URL and headers.
func ParseSourceStream(s ShowBoxSource) *StreamOption {
	if !IsM3U8Stream(s) {
		return nil
	}

	proxyURL := s.URL
	streamURL := proxyURL
	referer := DefaultFebboxReferer
	origin := DefaultFebboxOrigin
	userAgent := DefaultUserAgent

	if u, err := url.Parse(proxyURL); err == nil {
		q := u.Query()
		if inner := q.Get("url"); inner != "" {
			streamURL = inner
		}
		if ref := q.Get("referer"); ref != "" {
			if !strings.HasSuffix(ref, "/") {
				ref += "/"
			}
			referer = ref
		}
		if orig := q.Get("origin"); orig != "" {
			origin = strings.TrimRight(orig, "/")
		}
		if ua := q.Get("ua"); ua != "" {
			userAgent = ua
		}
	}

	// Double check direct stream is not an MKV or MP4 container
	lowerStream := strings.ToLower(streamURL)
	if strings.Contains(lowerStream, ".mkv") || strings.Contains(lowerStream, ".mp4") {
		return nil
	}

	expiry := ExtractExpiry(streamURL)
	if expiry == 0 {
		expiry = ExtractExpiry(proxyURL)
	}

	return &StreamOption{
		Name:        s.Name,
		Quality:     s.Quality,
		Type:        "M3U8",
		StreamURL:   streamURL,
		ProxyURL:    proxyURL,
		Referer:     referer,
		Origin:      origin,
		UserAgent:   userAgent,
		SizeGB:      ParseSizeInGB(s.Name),
		QualityRank: GetQualityRank(s.Quality),
		Expiry:      expiry,
	}
}

// SortStreams sorts stream options by QualityRank (descending), then SizeGB (descending).
func SortStreams(streams []StreamOption) {
	sort.Slice(streams, func(i, j int) bool {
		if streams[i].QualityRank != streams[j].QualityRank {
			return streams[i].QualityRank > streams[j].QualityRank
		}
		return streams[i].SizeGB > streams[j].SizeGB
	})
}

// SelectPreferredStream selects 1080p always if available;
// if 1080p is not in the response, it selects the highest available quality stream.
func SelectPreferredStream(streams []StreamOption) StreamOption {
	if len(streams) == 0 {
		return StreamOption{}
	}

	// 1. Check if 1080p is available (prefer 1080p always)
	var streams1080p []StreamOption
	for _, s := range streams {
		q := strings.ToLower(s.Quality)
		if strings.Contains(q, "1080") || s.QualityRank == 3 {
			streams1080p = append(streams1080p, s)
		}
	}

	if len(streams1080p) > 0 {
		// If multiple 1080p options exist, choose the highest file size / best quality one
		sort.Slice(streams1080p, func(i, j int) bool {
			return streams1080p[i].SizeGB > streams1080p[j].SizeGB
		})
		return streams1080p[0]
	}

	// 2. If 1080p is not in response, send the highest quality available
	sorted := make([]StreamOption, len(streams))
	copy(sorted, streams)
	SortStreams(sorted)
	return sorted[0]
}

// ResolveMovie resolves a movie TMDB ID via Tier 3 (ShowBox / FilmU).
func (t *Tier3) ResolveMovie(ctx context.Context, tmdbID int) (*models.ResolutionResult, error) {
	log.Printf("[tier3] resolving movie %d via ShowBox", tmdbID)

	title, streams, err := t.FetchAllM3U8MovieStreams(ctx, tmdbID)
	if err != nil || len(streams) == 0 {
		if err != nil {
			log.Printf("[tier3] movie %d error: %v", tmdbID, err)
		}
		return nil, nil // Cascade to clean 404
	}

	best := SelectPreferredStream(streams)
	log.Printf("[tier3] successfully resolved movie %d (%s) -> %s [%s, %.2f GB]", tmdbID, title, best.Quality, best.Name, best.SizeGB)

	var expPtr *int64
	if best.Expiry > 0 {
		exp := best.Expiry
		expPtr = &exp
	}

	return &models.ResolutionResult{
		Success: true,
		TmdbID:  tmdbID,
		Type:    "movie",
		Tier:    3,
		Source:  "showbox",
		Title:   title,
		URL:     best.StreamURL,
		Headers: map[string]string{
			"Origin":  DefaultFebboxOrigin,
			"Referer": DefaultFebboxReferer,
		},
		Expiry: expPtr,
	}, nil
}

// ResolveTV resolves a TV episode via Tier 3 (ShowBox / FilmU).
func (t *Tier3) ResolveTV(ctx context.Context, tmdbID, season, episode int) (*models.ResolutionResult, error) {
	log.Printf("[tier3] resolving TV %d S%dE%d via ShowBox", tmdbID, season, episode)

	title, streams, err := t.FetchAllM3U8TVStreams(ctx, tmdbID, season, episode)
	if err != nil || len(streams) == 0 {
		if err != nil {
			log.Printf("[tier3] TV %d S%dE%d error: %v", tmdbID, season, episode, err)
		}
		return nil, nil // Cascade to clean 404
	}

	best := SelectPreferredStream(streams)
	log.Printf("[tier3] successfully resolved TV %d S%dE%d (%s) -> %s [%s, %.2f GB]", tmdbID, season, episode, title, best.Quality, best.Name, best.SizeGB)

	var expPtr *int64
	if best.Expiry > 0 {
		exp := best.Expiry
		expPtr = &exp
	}

	return &models.ResolutionResult{
		Success: true,
		TmdbID:  tmdbID,
		Type:    "tv",
		Tier:    3,
		Source:  "showbox",
		Title:   title,
		Season:  &season,
		Episode: &episode,
		URL:     best.StreamURL,
		Headers: map[string]string{
			"Origin":  DefaultFebboxOrigin,
			"Referer": DefaultFebboxReferer,
		},
		Expiry: expPtr,
	}, nil
}

// FetchAllM3U8MovieStreams queries the API and returns all filtered M3U8 streams sorted by quality/size.
func (t *Tier3) FetchAllM3U8MovieStreams(ctx context.Context, tmdbID int) (string, []StreamOption, error) {
	tmdb, err := t.FetchTMDB(ctx, tmdbID, "movie")
	if err != nil || tmdb == nil || tmdb.Title == "" {
		return "", nil, fmt.Errorf("failed to fetch TMDB movie info: %w", err)
	}

	endpoint := fmt.Sprintf("%s/scrape/ShowBox/movie/%d", t.baseURL, tmdbID)
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return tmdb.Title, nil, err
	}

	q := reqURL.Query()
	q.Set("title", tmdb.Title)
	q.Set("febboxToken", t.febboxToken)
	reqURL.RawQuery = q.Encode()

	streams, err := t.executeScrapeRequest(ctx, reqURL.String())
	if err != nil {
		return tmdb.Title, nil, err
	}

	return tmdb.Title, streams, nil
}

// FetchAllM3U8TVStreams queries the API and returns all filtered M3U8 streams for a TV episode sorted by quality/size.
func (t *Tier3) FetchAllM3U8TVStreams(ctx context.Context, tmdbID, season, episode int) (string, []StreamOption, error) {
	tmdb, err := t.FetchTMDB(ctx, tmdbID, "tv")
	if err != nil || tmdb == nil || tmdb.Title == "" {
		return "", nil, fmt.Errorf("failed to fetch TMDB TV info: %w", err)
	}

	endpoint := fmt.Sprintf("%s/scrape/ShowBox/tv/%d", t.baseURL, tmdbID)
	reqURL, err := url.Parse(endpoint)
	if err != nil {
		return tmdb.Title, nil, err
	}

	q := reqURL.Query()
	q.Set("title", tmdb.Title)
	q.Set("season", strconv.Itoa(season))
	q.Set("episode", strconv.Itoa(episode))
	q.Set("febboxToken", t.febboxToken)
	reqURL.RawQuery = q.Encode()

	streams, err := t.executeScrapeRequest(ctx, reqURL.String())
	if err != nil {
		return tmdb.Title, nil, err
	}

	return tmdb.Title, streams, nil
}

// executeScrapeRequest performs the HTTP request to box.filmu.in and parses the returned sources.
func (t *Tier3) executeScrapeRequest(ctx context.Context, fullURL string) ([]StreamOption, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", t.apiKey)
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("showbox scraper returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var sbResp ShowBoxResponse
	if err := json.NewDecoder(resp.Body).Decode(&sbResp); err != nil {
		return nil, fmt.Errorf("failed to decode scraper response: %w", err)
	}

	var validStreams []StreamOption
	for _, src := range sbResp.Sources {
		opt := ParseSourceStream(src)
		if opt != nil {
			validStreams = append(validStreams, *opt)
		}
	}

	SortStreams(validStreams)
	return validStreams, nil
}

// GenerateM3U builds a clean, simple standard M3U playlist string containing strictly .m3u8 streams
// with only #EXTINF and direct URLs (no origin, user-agent, or referer directives), fully compatible with ExoPlayer, VLC, mpv, and all media players.
func (t *Tier3) GenerateM3U(title string, streams []StreamOption) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n\n")

	for idx, s := range streams {
		displayTitle := fmt.Sprintf("%s - %s %s [Option %d]", title, s.Quality, s.Name, idx+1)
		b.WriteString(fmt.Sprintf("#EXTINF:-1,%s\n", displayTitle))
		b.WriteString(s.StreamURL + "\n\n")
	}

	return b.String()
}

// GenerateSingleM3U builds a clean, simple standard M3U playlist string for a single stream.
func GenerateSingleM3U(title, streamURL, referer, origin, userAgent string) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n\n")
	b.WriteString(fmt.Sprintf("#EXTINF:-1,%s\n", title))
	b.WriteString(streamURL + "\n")

	return b.String()
}

// FetchTMDB fetches title and release metadata from TMDB API v3.
func (t *Tier3) FetchTMDB(ctx context.Context, tmdbID int, mediaType string) (*TMDBDetails, error) {
	if t.tmdbKey == "" || tmdbID <= 0 {
		return nil, fmt.Errorf("invalid tmdbKey or tmdbID")
	}

	reqURL := fmt.Sprintf("https://api.themoviedb.org/3/%s/%d?api_key=%s", mediaType, tmdbID, t.tmdbKey)
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "vidara-api/1.0")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tmdb api returned status %d", resp.StatusCode)
	}

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	titleVal, _ := data["title"].(string)
	if titleVal == "" {
		titleVal, _ = data["name"].(string)
	}
	if titleVal == "" {
		titleVal, _ = data["original_title"].(string)
	}
	if titleVal == "" {
		titleVal, _ = data["original_name"].(string)
	}

	dateVal, _ := data["release_date"].(string)
	if dateVal == "" {
		dateVal, _ = data["first_air_date"].(string)
	}
	yearVal := dateVal
	if strings.Contains(dateVal, "-") {
		yearVal = strings.Split(dateVal, "-")[0]
	}

	return &TMDBDetails{
		ID:    tmdbID,
		Title: titleVal,
		Year:  yearVal,
		Type:  mediaType,
	}, nil
}
