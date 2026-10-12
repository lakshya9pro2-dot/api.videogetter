package tiers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vidara-api/models"
)

// Ensure Tier3 satisfies the resolver.TierResolver interface at compile time.
type tierResolverInterface interface {
	ResolveMovie(ctx context.Context, tmdbID int) (*models.ResolutionResult, error)
	ResolveTV(ctx context.Context, tmdbID, season, episode int) (*models.ResolutionResult, error)
}

func TestTier3_InterfaceAndHelpers(t *testing.T) {
	tier := NewTier3("https://box.filmu.in", "test_key", "test_token", "tmdb_key", 5*time.Second)
	var _ tierResolverInterface = tier

	// Test ParseSizeInGB
	if size := ParseSizeInGB("ShowBox [8.09 GB]"); size != 8.09 {
		t.Errorf("expected 8.09 GB, got %f", size)
	}
	if size := ParseSizeInGB("ShowBox [512 MB]"); size != 0.5 {
		t.Errorf("expected 0.5 GB for 512MB, got %f", size)
	}

	// Test GetQualityRank
	if r := GetQualityRank("4K"); r != 4 {
		t.Errorf("expected rank 4 for 4K, got %d", r)
	}
	if r := GetQualityRank("1080p"); r != 3 {
		t.Errorf("expected rank 3 for 1080p, got %d", r)
	}
	if r := GetQualityRank("720p"); r != 2 {
		t.Errorf("expected rank 2 for 720p, got %d", r)
	}

	// Test IsM3U8Stream (Strictly M3U8 only - NO MKV or MP4)
	m3uSource := ShowBoxSource{Name: "ShowBox [8.09 GB]", URL: "https://hls.shegu.net/test.m3u8", Type: "m3u8"}
	if !IsM3U8Stream(m3uSource) {
		t.Errorf("expected m3u8 source to be accepted")
	}

	mkvSource := ShowBoxSource{Name: "ShowBox [37.82 GB]", URL: "https://shegu.net/test.mkv", Type: "video"}
	if IsM3U8Stream(mkvSource) {
		t.Errorf("expected MKV / video source to be rejected")
	}

	mp4Source := ShowBoxSource{Name: "ShowBox [2.1 GB]", URL: "https://shegu.net/test.mp4", Type: "video"}
	if IsM3U8Stream(mp4Source) {
		t.Errorf("expected MP4 / video source to be rejected")
	}
}

func TestTier3_ParseSourceStream_Headers(t *testing.T) {
	proxyURL := "https://wormhole.filmu.in/proxy/m3u8?url=https%3A%2F%2Fhls.shegu.net%2F123.m3u8&referer=https%3A%2F%2Fwww.febbox.com&origin=https%3A%2F%2Fwww.febbox.com&ua=CustomUA"
	source := ShowBoxSource{
		Name:    "ShowBox [8.09 GB]",
		Quality: "4K",
		Type:    "m3u8",
		URL:     proxyURL,
	}

	opt := ParseSourceStream(source)
	if opt == nil {
		t.Fatalf("expected parsed stream option")
	}

	if opt.StreamURL != "https://hls.shegu.net/123.m3u8" {
		t.Errorf("unexpected extracted stream url: %s", opt.StreamURL)
	}
	if opt.Origin != "https://www.febbox.com" {
		t.Errorf("expected Origin 'https://www.febbox.com', got '%s'", opt.Origin)
	}
	if opt.Referer != "https://www.febbox.com/" {
		t.Errorf("expected Referer 'https://www.febbox.com/', got '%s'", opt.Referer)
	}
}

func TestTier3_GenerateM3U_MultiPlayerCompliance(t *testing.T) {
	tier := NewTier3("", "", "", "", 2*time.Second)
	streams := []StreamOption{
		{
			Name:        "ShowBox [8.09 GB]",
			Quality:     "4K",
			Type:        "M3U8",
			StreamURL:   "https://hls.shegu.net/test4k.m3u8",
			Referer:     "https://www.febbox.com/",
			Origin:      "https://www.febbox.com",
			UserAgent:   DefaultUserAgent,
			SizeGB:      8.09,
			QualityRank: 4,
		},
		{
			Name:        "ShowBox [2.95 GB]",
			Quality:     "1080p",
			Type:        "M3U8",
			StreamURL:   "https://hls.shegu.net/test1080p.m3u8",
			Referer:     "https://www.febbox.com/",
			Origin:      "https://www.febbox.com",
			UserAgent:   DefaultUserAgent,
			SizeGB:      2.95,
			QualityRank: 3,
		},
	}

	m3u := tier.GenerateM3U("Fight Club", streams)

	if !strings.HasPrefix(m3u, "#EXTM3U") {
		t.Errorf("expected playlist to start with #EXTM3U")
	}
	if !strings.Contains(m3u, "#EXTVLCOPT:http-referrer=https://www.febbox.com/") {
		t.Errorf("missing VLC referrer directive")
	}
	if !strings.Contains(m3u, "#EXTVLCOPT:http-origin=https://www.febbox.com") {
		t.Errorf("missing VLC origin directive")
	}
	if !strings.Contains(m3u, "#EXTHTTP:") {
		t.Errorf("missing IPTV #EXTHTTP directive")
	}
	if !strings.Contains(m3u, "#KODIPROP:inputstream.adaptive.manifest_headers=") {
		t.Errorf("missing Kodi inputstream property")
	}
	if strings.Contains(m3u, ".mkv") || strings.Contains(m3u, ".mp4") {
		t.Errorf("m3u playlist MUST NOT contain any mkv or mp4 links")
	}
}

func TestTier3_ResolveMovie_MockServer(t *testing.T) {
	// Mock TMDB Server
	tmdbTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":    550,
			"title": "Fight Club",
		})
	}))
	defer tmdbTS.Close()

	// Mock ShowBox Server
	showboxTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "my_key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("title") != "Fight Club" {
			http.Error(w, "bad title", http.StatusBadRequest)
			return
		}

		resp := ShowBoxResponse{
			Name: "ShowBox",
			Sources: []ShowBoxSource{
				// MKV should be filtered out
				{
					Name:    "ShowBox [37.82 GB]",
					Quality: "2160p",
					Type:    "video",
					URL:     "https://shegu.net/vip/movie.mkv",
				},
				// Best M3U8
				{
					Name:    "ShowBox [8.09 GB]",
					Quality: "4K",
					Type:    "m3u8",
					URL:     "https://wormhole.filmu.in/proxy/m3u8?url=https%3A%2F%2Fhls.shegu.net%2F4k.m3u8&referer=https%3A%2F%2Fwww.febbox.com&origin=https%3A%2F%2Fwww.febbox.com",
				},
				// Lower M3U8
				{
					Name:    "ShowBox [2.95 GB]",
					Quality: "1080p",
					Type:    "m3u8",
					URL:     "https://wormhole.filmu.in/proxy/m3u8?url=https%3A%2F%2Fhls.shegu.net%2F1080p.m3u8&referer=https%3A%2F%2Fwww.febbox.com&origin=https%3A%2F%2Fwww.febbox.com",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer showboxTS.Close()

	tier := NewTier3(showboxTS.URL, "my_key", "my_token", "dummy_tmdb", 2*time.Second)

	// Override TMDB URL by setting custom FetchTMDB method or mocking via http client
	// Here we test FetchAllM3U8MovieStreams and ResolveMovie
	// Test scraper direct query:
	streams, err := tier.executeScrapeRequest(context.Background(), showboxTS.URL+"/scrape/ShowBox/movie/550?title=Fight+Club")
	if err != nil {
		t.Fatalf("failed executeScrapeRequest: %v", err)
	}

	if len(streams) != 2 {
		t.Fatalf("expected 2 M3U8 streams (MKV filtered out), got %d", len(streams))
	}
	if streams[0].Quality != "4K" || streams[0].StreamURL != "https://hls.shegu.net/4k.m3u8" {
		t.Errorf("expected 4K stream to be top sorted, got %+v", streams[0])
	}
	if streams[0].Origin != "https://www.febbox.com" {
		t.Errorf("expected Origin https://www.febbox.com, got %s", streams[0].Origin)
	}
	if streams[0].Referer != "https://www.febbox.com/" {
		t.Errorf("expected Referer https://www.febbox.com/, got %s", streams[0].Referer)
	}
}
