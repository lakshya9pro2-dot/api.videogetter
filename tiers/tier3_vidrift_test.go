package tiers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestVidriftTier_Priority1_EvionURL(t *testing.T) {
	// Mock server that returns embed HTML with evionUrl present
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/embed/") {
			html := `<!DOCTYPE html><html><head><script>
var embedMeta = {
  "tmdbId": "1396",
  "type": "tv",
  "season": 1,
  "episode": 1,
  "playbackToken": "test_token_123",
  "evionUrl": "https://img.animecurx.tech/tvshows/afe8c1475da4885935a8/index.m3u8"
};
</script></head><body></body></html>`
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(html))
			return
		}
		// If API is called, fail the test because evionUrl should short-circuit API calls
		t.Errorf("API should NOT be called when evionUrl is present in HTML! Called: %s", r.URL.Path)
		http.Error(w, "should not call api", http.StatusBadRequest)
	}))
	defer ts.Close()

	tier := NewVidriftTier(ts.URL, "", 2*time.Second)

	streamURL, provider, err := tier.ResolveStream(context.Background(), "tv/1396/1/1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider != "evion" {
		t.Errorf("expected provider 'evion', got %q", provider)
	}
	if streamURL != "https://img.animecurx.tech/tvshows/afe8c1475da4885935a8/index.m3u8" {
		t.Errorf("unexpected streamURL: %s", streamURL)
	}

	// Test ResolveTV
	resTV, errTV := tier.ResolveTV(context.Background(), 1396, 1, 1)
	if errTV != nil || resTV == nil || !resTV.Success {
		t.Fatalf("ResolveTV failed: %v, %+v", errTV, resTV)
	}
	if resTV.Tier != 3 || resTV.Source != "vidrift-evion" {
		t.Errorf("unexpected Tier/Source: %+v", resTV)
	}
	if resTV.URL != "https://img.animecurx.tech/tvshows/afe8c1475da4885935a8/index.m3u8" {
		t.Errorf("unexpected URL in resTV: %s", resTV.URL)
	}
}

func TestVidriftTier_Priority2_AtlasFallback(t *testing.T) {
	apiCalled := false

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/embed/") {
			// evionUrl is empty, forcing fallback to API
			html := `<!DOCTYPE html><html><head><script>
var embedMeta = {
  "tmdbId": "550",
  "type": "movie",
  "playbackToken": "test_token_atlas",
  "evionUrl": ""
};
</script></head><body></body></html>`
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(html))
			return
		}

		if strings.HasPrefix(r.URL.Path, "/api/source/") {
			prov := r.URL.Query().Get("provider")
			if prov == "atlas" {
				apiCalled = true
				resp := `{
					"success": true,
					"tmdbId": "550",
					"type": "movie",
					"provider": "atlas",
					"streams": [
						{
							"index": 0,
							"proxyUrl": "https://edge.reelvault.click/proxy?u=atlas_stream_key",
							"type": "hls",
							"provider": "Atlas"
						}
					]
				}`
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(resp))
				return
			}
		}

		http.NotFound(w, r)
	}))
	defer ts.Close()

	tier := NewVidriftTier(ts.URL, "", 2*time.Second)

	streamURL, provider, err := tier.ResolveStream(context.Background(), "movie/550")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !apiCalled {
		t.Errorf("expected atlas API to be called")
	}
	if provider != "atlas" {
		t.Errorf("expected provider 'atlas', got %q", provider)
	}
	if streamURL != "https://edge.reelvault.click/proxy?u=atlas_stream_key" {
		t.Errorf("unexpected streamURL: %s", streamURL)
	}

	// Test ResolveMovie
	resM, errM := tier.ResolveMovie(context.Background(), 550)
	if errM != nil || resM == nil || !resM.Success {
		t.Fatalf("ResolveMovie failed: %v, %+v", errM, resM)
	}
	if resM.Tier != 3 || resM.Source != "vidrift-atlas" {
		t.Errorf("unexpected Tier/Source: %+v", resM)
	}
}

func TestVidriftTier_Priority3_VelaFallback(t *testing.T) {
	atlasAttempted := false
	velaCalled := false

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/embed/") {
			// evionUrl is empty
			html := `<!DOCTYPE html><html><head><script>
var embedMeta = {
  "tmdbId": "550",
  "type": "movie",
  "playbackToken": "test_token_vela",
  "evionUrl": ""
};
</script></head><body></body></html>`
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(html))
			return
		}

		if strings.HasPrefix(r.URL.Path, "/api/source/") {
			prov := r.URL.Query().Get("provider")
			if prov == "atlas" {
				atlasAttempted = true
				// Atlas fails (e.g. 500 or no streams)
				http.Error(w, "atlas internal error", http.StatusInternalServerError)
				return
			}
			if prov == "vela" {
				velaCalled = true
				resp := `{
					"success": true,
					"tmdbId": "550",
					"type": "movie",
					"provider": "vela",
					"streams": [
						{
							"index": 0,
							"proxyUrl": "https://edge.reelvault.click/proxy?u=vela_stream_fmp4",
							"type": "hls",
							"provider": "Vela"
						}
					]
				}`
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(resp))
				return
			}
		}

		http.NotFound(w, r)
	}))
	defer ts.Close()

	tier := NewVidriftTier(ts.URL, "", 2*time.Second)

	streamURL, provider, err := tier.ResolveStream(context.Background(), "movie/550")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !atlasAttempted {
		t.Errorf("expected atlas to be attempted first")
	}
	if !velaCalled {
		t.Errorf("expected vela to be called after atlas failed")
	}
	if provider != "vela" {
		t.Errorf("expected provider 'vela', got %q", provider)
	}
	if streamURL != "https://edge.reelvault.click/proxy?u=vela_stream_fmp4" {
		t.Errorf("unexpected streamURL: %s", streamURL)
	}
}

func TestVidriftTier_EasyExtension_AddProvider(t *testing.T) {
	tier := NewVidriftTier("https://embed.vidrift.in", "", 2*time.Second)

	initial := tier.GetAPIProviders()
	if len(initial) != 2 || initial[0] != "atlas" || initial[1] != "vela" {
		t.Errorf("unexpected default providers: %v", initial)
	}

	// Add custom provider
	tier.AddAPIProvider("nova")
	updated := tier.GetAPIProviders()
	if len(updated) != 3 || updated[2] != "nova" {
		t.Errorf("AddAPIProvider failed, got %v", updated)
	}

	// Override providers
	tier.SetAPIProviders("custom1", "custom2")
	custom := tier.GetAPIProviders()
	if len(custom) != 2 || custom[0] != "custom1" || custom[1] != "custom2" {
		t.Errorf("SetAPIProviders failed, got %v", custom)
	}
}

func TestVidriftTier_CineTV_MovieSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/movie/550" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"550","name":"Fight Club","url":"http://filmin.a0p3.com/vod/1/test.mp4?wsSecret=abc"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	tier := NewVidriftTier("https://embed.vidrift.in", "", 2*time.Second)
	tier.SetCineTVBaseURL(ts.URL)

	res, err := tier.ResolveMovie(context.Background(), 550)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil || !res.Success {
		t.Fatalf("expected successful resolution, got: %+v", res)
	}
	if res.Source != "cinetv" {
		t.Errorf("expected source 'cinetv', got %q", res.Source)
	}
	if !res.Direct {
		t.Errorf("expected Direct=true for cinetv")
	}
	if res.StreamType != "mp4" {
		t.Errorf("expected StreamType='mp4', got %q", res.StreamType)
	}
	if res.URL != "https://filmin.a0p3.com/vod/1/test.mp4?wsSecret=abc" {
		t.Errorf("expected upgraded https URL, got %q", res.URL)
	}
	if res.Title != "Fight Club" {
		t.Errorf("expected title 'Fight Club', got %q", res.Title)
	}
}

func TestVidriftTier_CineTV_TVSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tv/1399/1/6" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"1399","name":"Game of Thrones","season":"1","episode":"6","url":"http://filmin.a0p3.com/vod/1/got.mp4?wsSecret=xyz"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	tier := NewVidriftTier("https://embed.vidrift.in", "", 2*time.Second)
	tier.SetCineTVBaseURL(ts.URL)

	res, err := tier.ResolveTV(context.Background(), 1399, 1, 6)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil || !res.Success {
		t.Fatalf("expected successful resolution, got: %+v", res)
	}
	if res.Source != "cinetv" {
		t.Errorf("expected source 'cinetv', got %q", res.Source)
	}
	if !res.Direct || res.StreamType != "mp4" {
		t.Errorf("expected Direct=true and StreamType='mp4', got: %+v", res)
	}
	if res.URL != "https://filmin.a0p3.com/vod/1/got.mp4?wsSecret=xyz" {
		t.Errorf("expected upgraded https URL, got %q", res.URL)
	}
	if *res.Season != 1 || *res.Episode != 6 {
		t.Errorf("unexpected season/episode: %v/%v", *res.Season, *res.Episode)
	}
}

func TestVidriftTier_CineTV_FallbackToVidrift(t *testing.T) {
	// CineTV returns 404, Vidrift mock returns evionUrl
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/movie/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/embed/movie/") {
			html := `<!DOCTYPE html><html><head><script>
var embedMeta = {
  "tmdbId": "999",
  "type": "movie",
  "evionUrl": "https://img.animecurx.tech/movies/fallback.m3u8"
};
</script></head><body></body></html>`
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(html))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	tier := NewVidriftTier(ts.URL, "", 2*time.Second)
	tier.SetCineTVBaseURL(ts.URL)

	res, err := tier.ResolveMovie(context.Background(), 999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil || !res.Success {
		t.Fatalf("expected successful fallback, got %+v", res)
	}
	if res.Source != "vidrift-evion" {
		t.Errorf("expected source 'vidrift-evion', got %q", res.Source)
	}
	if res.URL != "https://img.animecurx.tech/movies/fallback.m3u8" {
		t.Errorf("unexpected fallback URL: %s", res.URL)
	}
}
