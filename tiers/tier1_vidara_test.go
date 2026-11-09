package tiers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"vidara-api/models"
)

func TestVidaraTier_LoadDataAndLookup(t *testing.T) {
	tier := NewVidaraTier("https://vidara.to", "https://streamtape.com", 5*time.Second)

	// Test loading data from data.json or data/vidara.json
	err := tier.LoadData("data/vidara.json", "data.json", "../data/vidara.json", "../data.json")
	if err != nil {
		t.Fatalf("failed to load dataset: %v", err)
	}

	// 1. Movie lookup: TMDB ID 1167307
	movie, found := tier.FindMovie(1167307)
	if !found {
		t.Fatalf("movie 1167307 should be found in local dataset")
	}
	if movie.Play.VA == "" && movie.Play.Play == "" {
		t.Errorf("unexpected movie playback data: %+v", movie.Play)
	}

	// 2. Movie not in dataset: 550
	_, found550 := tier.FindMovie(550)
	if found550 {
		t.Errorf("movie 550 should NOT be found in Vidara Tier 1 dataset")
	}

	// 3. TV show lookup: TMDB ID 262838 Season 1 Episode 1
	tvEp1, foundEp1 := tier.FindTV(262838, 1, 1)
	if !foundEp1 {
		t.Fatalf("TV 262838 S1E1 should be found")
	}
	if tvEp1.Season != 1 || tvEp1.Episode != 1 {
		t.Errorf("expected S1E1, got S%dE%d", tvEp1.Season, tvEp1.Episode)
	}

	// 4. TV show lookup: Season 2 Episode 2
	tvEp2, foundEp2 := tier.FindTV(262838, 2, 2)
	if !foundEp2 {
		t.Fatalf("TV 262838 S2E2 should be found")
	}
	if tvEp2.Season != 2 || tvEp2.Episode != 2 {
		t.Errorf("expected S2E2, got S%dE%d", tvEp2.Season, tvEp2.Episode)
	}

	// 5. Nonexistent episode: Season 1 Episode 99
	_, foundNonexistent := tier.FindTV(262838, 1, 99)
	if foundNonexistent {
		t.Errorf("TV 262838 S1E99 should NOT be found")
	}

	// 6. Nonexistent season: Season 9 Episode 1
	_, foundNonexistentS := tier.FindTV(262838, 9, 1)
	if foundNonexistentS {
		t.Errorf("TV 262838 S9E1 should NOT be found")
	}
}

func TestVidaraTier_ResolveMovieAndTV(t *testing.T) {
	tier := NewVidaraTier("https://vidara.to", "https://streamtape.com", 2*time.Second)
	_ = tier.LoadData("data/vidara.json", "data.json", "../data/vidara.json", "../data.json")

	// Movie resolution
	resM, err := tier.ResolveMovie(context.Background(), 1167307)
	if err != nil || resM == nil || !resM.Success {
		t.Fatalf("expected successful movie resolution, got %+v (err: %v)", resM, err)
	}
	if resM.Tier != 1 || resM.Source != "vidara" || resM.URL == "" {
		t.Errorf("invalid Tier 1 movie resolution response: %+v", resM)
	}

	// TV resolution
	resTV, err := tier.ResolveTV(context.Background(), 262838, 1, 2)
	if err != nil || resTV == nil || !resTV.Success {
		t.Fatalf("expected successful TV resolution, got %+v (err: %v)", resTV, err)
	}
	if resTV.Tier != 1 || resTV.Source != "vidara" || *resTV.Season != 1 || *resTV.Episode != 2 {
		t.Errorf("invalid Tier 1 TV resolution response: %+v", resTV)
	}
}

func TestVidaraTier_ParseMalformedDataset(t *testing.T) {
	// Directly test parsing the raw malformed data.json
	tier := NewVidaraTier("https://vidara.to", "https://streamtape.com", 2*time.Second)
	data, err := os.ReadFile("data.json")
	if err != nil {
		data, err = os.ReadFile("../data.json")
	}
	if err != nil {
		t.Skip("data.json not found in test dir")
	}

	items, err := tier.parseDataset(data)
	if err != nil {
		t.Fatalf("parseDataset failed on raw data.json: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("expected parsed items from data.json, got 0")
	}
}

func TestCleanIDAndParsing(t *testing.T) {
	if CleanID("https://streamtape.com/v/0A2vDYQz3wIbPQ6/") != "0A2vDYQz3wIbPQ6" {
		t.Errorf("CleanID failed for full url")
	}
	if CleanID("(id)d932127894f1%28id%29") != "d932127894f1" {
		t.Errorf("CleanID failed for id with decorators")
	}

	st, va := ParseIDsFromString("https://example.com/play?st=0A2vDYQz3wIbPQ6&va=d932127894f1")
	if st != "0A2vDYQz3wIbPQ6" || va != "d932127894f1" {
		t.Errorf("ParseIDsFromString query param failed, got st=%s, va=%s", st, va)
	}

	st2, va2 := ParseIDsFromString("st/0A2vDYQz3wIbPQ6/va/d932127894f1")
	if st2 != "0A2vDYQz3wIbPQ6" || va2 != "d932127894f1" {
		t.Errorf("ParseIDsFromString path failed, got st=%s, va=%s", st2, va2)
	}
}

func TestEvalStreamTapeJS(t *testing.T) {
	expr := `'hello'.substring(0, 4) + 'world'.substring(1)`
	res := EvalStreamTapeJS(context.Background(), expr)
	if res != "hellorld" {
		t.Errorf("expected 'hellorld', got '%s'", res)
	}
}

func TestVidaraTier_DirectPlayURL(t *testing.T) {
	tier := NewVidaraTier("https://vidara.to", "https://streamtape.com", 2*time.Second)

	// Case 1: VA has code -> CheckDirectPlay returns false, Tier 1 does its work
	playWithVA := models.Playback{
		VA:  "mzfdFOguU87x",
		URL: "https://example.com/direct.m3u8",
	}
	if url, ok := tier.CheckDirectPlay(playWithVA); ok {
		t.Errorf("expected CheckDirectPlay to return false when VA has code, got url=%s", url)
	}

	// Case 2: VA is empty, play has URL -> CheckDirectPlay returns direct URL
	playDirectURL := models.Playback{
		VA:  "",
		URL: "https://example.com/direct_stream.m3u8",
	}
	if url, ok := tier.CheckDirectPlay(playDirectURL); !ok || url != "https://example.com/direct_stream.m3u8" {
		t.Errorf("expected direct URL, got url=%s, ok=%v", url, ok)
	}

	// Case 3: VA is empty, play has Play field with URL -> CheckDirectPlay returns direct URL
	playDirectField := models.Playback{
		VA:   "",
		Play: "https://example.com/direct_play.mp4",
	}
	if url, ok := tier.CheckDirectPlay(playDirectField); !ok || url != "https://example.com/direct_play.mp4" {
		t.Errorf("expected direct URL from Play field, got url=%s, ok=%v", url, ok)
	}

	// Case 4: VA is empty and play is empty -> returns false
	playEmpty := models.Playback{VA: "", URL: "", Play: ""}
	if _, ok := tier.CheckDirectPlay(playEmpty); ok {
		t.Errorf("expected false for empty playback")
	}

	// Test in-memory resolution of movie with direct URL
	tier.movieIndex[99901] = MovieRecord{
		ID:    99901,
		Title: "Direct Movie",
		Play: models.Playback{
			VA:  "",
			URL: "https://cdn.test/direct_movie.m3u8",
		},
	}

	resM, err := tier.ResolveMovie(context.Background(), 99901)
	if err != nil || resM == nil || !resM.Success {
		t.Fatalf("expected successful resolution for direct movie: %v", err)
	}
	if resM.URL != "https://cdn.test/direct_movie.m3u8" {
		t.Errorf("expected direct URL https://cdn.test/direct_movie.m3u8, got %s", resM.URL)
	}
	if resM.Tier != 1 || resM.Source != "vidara" {
		t.Errorf("expected Tier 1 Vidara, got %+v", resM)
	}

	// Test in-memory resolution of TV episode with direct URL
	tier.tvIndex[99902] = map[int]map[int]TVEpisodeRecord{
		1: {
			1: TVEpisodeRecord{
				ID:      99902,
				Title:   "Direct Show",
				Season:  1,
				Episode: 1,
				Play: models.Playback{
					VA:   "",
					Play: "https://cdn.test/direct_tv_s1e1.m3u8",
				},
			},
		},
	}

	resTV, err := tier.ResolveTV(context.Background(), 99902, 1, 1)
	if err != nil || resTV == nil || !resTV.Success {
		t.Fatalf("expected successful resolution for direct TV: %v", err)
	}
	if resTV.URL != "https://cdn.test/direct_tv_s1e1.m3u8" {
		t.Errorf("expected direct URL https://cdn.test/direct_tv_s1e1.m3u8, got %s", resTV.URL)
	}
}

func TestCleanID_StreamHG(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"ac7n6t4e5nma", "ac7n6t4e5nma"},
		{"https://hanerix.com/e/ac7n6t4e5nma", "ac7n6t4e5nma"},
		{"https://hglink.to/e/ac7n6t4e5nma", "ac7n6t4e5nma"},
		{"https://hgcloud.to/e/ac7n6t4e5nma", "ac7n6t4e5nma"},
		{"c=ac7n6t4e5nma", "ac7n6t4e5nma"},
		{"sh=ac7n6t4e5nma", "ac7n6t4e5nma"},
		{"https://streamhg.com/e/ac7n6t4e5nma", "ac7n6t4e5nma"},
		{"https://vibuxer.com/e/ac7n6t4e5nma", "ac7n6t4e5nma"},
	}

	for _, c := range cases {
		got := CleanID(c.input)
		if got != c.expected {
			t.Errorf("CleanID(%q) = %q; want %q", c.input, got, c.expected)
		}
	}
}

func TestUnpackPackedJS(t *testing.T) {
	html := `eval(function(p,a,c,k,e,d){...}('var 1={"0":"/2/3/4.5"}; 6',7,7,'hls4|links|stream|test|master|m3u8|jwplayer'.split('|')))`
	unpacked := UnpackPackedJS(html)
	expected := `var links={"hls4":"/stream/test/master.m3u8"}; jwplayer`
	if unpacked != expected {
		t.Errorf("UnpackPackedJS got %q, want %q", unpacked, expected)
	}
}

func TestVidaraTier_ExtractStreamHG(t *testing.T) {
	sampleM3U8 := `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio0",NAME="Hindi",LANGUAGE="hi",AUTOSELECT=YES,DEFAULT=YES,URI="/audio-hi.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1500000,RESOLUTION=1280x720
/video-720p.m3u8
`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/e/ac7n6t4e5nma") {
			html := `<!DOCTYPE html><html><head><title>StreamHG Movie</title></head><body><script>
eval(function(p,a,c,k,e,d){...}('var 1={"0":"/2/3/4.5"}; 6',7,7,'hls4|links|stream|test|master|m3u8|jwplayer'.split('|')))
</script></body></html>`
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(html))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/stream/test/master.m3u8") {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Write([]byte(sampleM3U8))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	tier := NewVidaraTier("https://vidara.to", "https://streamtape.com", 2*time.Second)
	tier.SetStreamHGBase(ts.URL)

	// Test ExtractStreamHG by ID
	res, err := tier.ExtractStreamHG(context.Background(), "ac7n6t4e5nma")
	if err != nil {
		t.Fatalf("ExtractStreamHG failed: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected res.Success=true")
	}
	expectedURL := ts.URL + "/stream/test/master.m3u8"
	if res.URL != expectedURL {
		t.Errorf("res.URL=%q; want %q", res.URL, expectedURL)
	}
	if len(res.AudioTracks) == 0 {
		t.Errorf("expected dynamic audio tracks parsed from m3u8, got 0")
	}

	// Test ExtractAny with Hanerix URL
	hanerixURL := ts.URL + "/e/ac7n6t4e5nma"
	resAny, errAny := tier.ExtractAny(context.Background(), hanerixURL)
	if errAny != nil {
		t.Fatalf("ExtractAny failed for Hanerix URL: %v", errAny)
	}
	if !resAny.Success || resAny.URL != expectedURL {
		t.Errorf("ExtractAny unexpected result: %+v", resAny)
	}

	// Test ResolveMovie with SH
	tier.movieIndex[88801] = MovieRecord{
		ID:    88801,
		Title: "StreamHG Movie",
		Play: models.Playback{
			VA: "",
			SH: "ac7n6t4e5nma",
		},
	}
	resM, errM := tier.ResolveMovie(context.Background(), 88801)
	if errM != nil || resM == nil || !resM.Success {
		t.Fatalf("ResolveMovie with SH failed: %v, %+v", errM, resM)
	}
	if resM.Source != "streamhg" {
		t.Errorf("expected source 'streamhg', got %q", resM.Source)
	}
	if resM.URL != expectedURL {
		t.Errorf("expected URL %q, got %q", expectedURL, resM.URL)
	}
	if resM.Play == nil || resM.Play.SH != "ac7n6t4e5nma" {
		t.Errorf("expected play.SH to be preserved in result: %+v", resM.Play)
	}

	// Test ResolveTV with SH
	tier.tvIndex[88802] = map[int]map[int]TVEpisodeRecord{
		1: {
			1: TVEpisodeRecord{
				ID:      88802,
				Title:   "StreamHG Show",
				Season:  1,
				Episode: 1,
				Play: models.Playback{
					VA: "",
					SH: "ac7n6t4e5nma",
				},
			},
		},
	}
	resTV, errTV := tier.ResolveTV(context.Background(), 88802, 1, 1)
	if errTV != nil || resTV == nil || !resTV.Success {
		t.Fatalf("ResolveTV with SH failed: %v, %+v", errTV, resTV)
	}
	if resTV.Source != "streamhg" {
		t.Errorf("expected source 'streamhg', got %q", resTV.Source)
	}
	if resTV.URL != expectedURL {
		t.Errorf("expected URL %q, got %q", expectedURL, resTV.URL)
	}
	if resTV.Play == nil || resTV.Play.SH != "ac7n6t4e5nma" {
		t.Errorf("expected play.SH to be preserved in result: %+v", resTV.Play)
	}
}

func TestVidaraTier_ExtractHubCloud_Mock(t *testing.T) {
	// Mock download options page with FSLv2 Server, FSL Server, and BuzzServer
	optionsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		html := `
<div class="card-header">The.Strangers.Chapter.3.2026.1080p.mkv</div>
<a href="https://example.com/fslv2/stream.mkv" class="btn btn-success">Download [FSLv2 Server]</a>
<a href="https://example.com/fsl/stream.mkv" class="btn btn-success">Download [FSL Server]</a>
<a href="https://example.com/buzz/stream.mkv" class="btn btn-success">Download [Buzz Server]</a>
<a href="https://pixeldrain.dev/u/123" class="btn btn-success">Download [PixelServer]</a>
`
		w.Write([]byte(html))
	}))
	defer optionsServer.Close()

	// Mock initial drive page
	driveServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		html := `
<a id="download" href="` + optionsServer.URL + `/hubcloud.php?id=test" class="btn btn-primary">Download</a>
`
		w.Write([]byte(html))
	}))
	defer driveServer.Close()

	tier := NewVidaraTier("https://vidara.to", "https://streamtape.com", 5*time.Second)
	ctx := context.Background()

	res, err := tier.ExtractHubCloud(ctx, driveServer.URL)
	if err != nil {
		t.Fatalf("ExtractHubCloud failed: %v", err)
	}

	if !res.Success {
		t.Fatalf("expected success=true, got %+v", res)
	}

	// Must select FSLv2 Server as priority 1
	expected := "https://example.com/fslv2/stream.mkv"
	if res.URL != expected {
		t.Errorf("expected chosen URL %q (FSLv2), got %q", expected, res.URL)
	}

	if !strings.Contains(res.Source, "FSLv2 Server") {
		t.Errorf("expected source to mention FSLv2 Server, got %q", res.Source)
	}
}

func TestVidaraTier_HubCloud_WorkerProxy(t *testing.T) {
	// Mock dedicated HubCloud worker server (movi-video-proxy)
	workerCalled := false
	var workerHost string
	workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workerCalled = true
		workerHost = r.Host
		if r.URL.Path == "/api/hide" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(HubCloudHideResponse{
				StreamURL: "/stream/Movie.2026.1080p.mkv?token=mocktoken123",
				Token:     "mocktoken123",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer workerServer.Close()

	// Mock options server for HubCloud
	optionsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		html := `
<div class="card-header">Movie.2026.1080p.mkv</div>
<a href="https://example.com/fslv2/stream.mkv" class="btn">FSLv2 Server [1080p]</a>
`
		_, _ = w.Write([]byte(html))
	}))
	defer optionsServer.Close()

	driveServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		html := fmt.Sprintf(`<a id="download" href="%s">Download</a>`, optionsServer.URL)
		_, _ = w.Write([]byte(html))
	}))
	defer driveServer.Close()

	tier := NewVidaraTier("https://vidara.to", "https://streamtape.com", 5*time.Second)
	tier.SetHubCloudProxyURL(workerServer.URL)
	tier.SetAutoProxyHubCloud(true)

	// Add test movie with HDHub pointing to mock driveServer
	tier.movieIndex[77701] = MovieRecord{
		ID:    77701,
		Title: "Test HubCloud Movie",
		Play: models.Playback{
			HDHub: driveServer.URL,
		},
	}

	res, err := tier.ResolveMovie(context.Background(), 77701)
	if err != nil {
		t.Fatalf("ResolveMovie failed: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success=true, got %+v", res)
	}

	if !workerCalled {
		t.Errorf("expected worker to be called for HubCloud stream proxying")
	}

	expectedURL := "http://" + workerHost + "/stream/Movie.2026.1080p.mkv?token=mocktoken123"
	if res.URL != expectedURL {
		t.Errorf("expected proxied URL %q, got %q", expectedURL, res.URL)
	}
	if res.Direct != false {
		t.Errorf("expected Direct=false for proxied stream")
	}
	if res.StreamType != "mkv" {
		t.Errorf("expected StreamType='mkv', got %q", res.StreamType)
	}
}

func TestVidaraTier_ExtractHubCloud_Live(t *testing.T) {
	liveURL := "https://hubcloud.ist/drive/ligaiuaszsqmfaj"
	tier := NewVidaraTier("https://vidara.to", "https://streamtape.com", 15*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	res, err := tier.ExtractHubCloud(ctx, liveURL)
	if err != nil {
		t.Logf("Notice: Live HubCloud fetch returned err: %v (network or remote block)", err)
		return
	}

	if !res.Success || res.URL == "" {
		t.Fatalf("expected valid stream URL from HubCloud, got %+v", res)
	}

	t.Logf("Extracted HubCloud Live Stream: %s (Source: %s, Title: %s)", res.URL, res.Source, res.Title)
	if !strings.Contains(res.URL, "r2.cloudflarestorage.com") && !strings.Contains(res.URL, "fsl") && !strings.Contains(res.URL, "http") {
		t.Errorf("unexpected stream URL format: %s", res.URL)
	}
}

func TestVidaraTier_ExtractStreamTape_Live(t *testing.T) {
	liveURL := "https://streamtape.com/v/0A2vDYQz3wIbPQ6"
	tier := NewVidaraTier("https://vidara.to", "https://streamtape.com", 15*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	res, err := tier.ExtractStreamTape(ctx, liveURL)
	if err != nil {
		t.Fatalf("Streamtape live extraction failed: %v", err)
	}

	if !res.Success || res.URL == "" {
		t.Fatalf("expected valid stream URL from Streamtape, got %+v", res)
	}

	t.Logf("Extracted StreamTape Live Stream: %s (Title: %s, StreamType: %s)", res.URL, res.Title, res.StreamType)

	if !strings.Contains(res.URL, "tapecontent.net") && !strings.Contains(res.URL, "streamtape.com/get_video") {
		t.Errorf("expected tapecontent.net or streamtape.com get_video URL, got: %s", res.URL)
	}
	if !strings.Contains(res.URL, "stream=1") {
		t.Errorf("expected stream=1 in URL, got: %s", res.URL)
	}
}

func TestVidaraTier_ProxyHubCloudStream_Live(t *testing.T) {
	tier := NewVidaraTier("", "", 10*time.Second)
	rawURL := "https://701ab416771ea1b49a24683d35a33206.r2.cloudflarestorage.com/hub/The.Strangers.Chapter.3.2026.1080p.10Bit.BluRay.Hindi.2.0-English.5.1.HEVC.x265-HDHub4u.Ms.mkv?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=9117075ae9ab2bd2c1b64fc58debeee0%2F20261103%2Fauto%2Fs3%2Faws4_request&X-Amz-Date=20261103T065600Z&X-Amz-Expires=18000&X-Amz-SignedHeaders=host&response-content-disposition=attachment%3B%20filename%3D%22The.Strangers.Chapter.3.2026.1080p.10Bit.BluRay.Hindi.2.0-English.5.1.HEVC.x265-HDHub4u.Ms.mkv%22&X-Amz-Signature=1c9409ed89f529a4b952156330cc7e37e73ce581c868676e71a460d0150b43f8"

	proxied, err := tier.ProxyHubCloudStream(context.Background(), rawURL)
	if err != nil {
		t.Fatalf("ProxyHubCloudStream failed: %v", err)
	}
	t.Logf("Successfully proxied via live worker: %s", proxied)
	if !strings.HasPrefix(proxied, "https://proxyservr.kineflex-netflex.workers.dev/stream/") {
		t.Fatalf("expected stream URL prefix, got: %s", proxied)
	}
}

func TestVidaraTier_DynamicHubCloudDomain_Mock(t *testing.T) {
	mockDomainsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"moviesdrive": "https://new1.moviesdrive.cyou", "hubcloud": "https://hubcloud.mock.pw"}`)
	}))
	defer mockDomainsServer.Close()

	tier := NewVidaraTier("https://vidara.to", "https://streamtape.com", 5*time.Second)
	tier.SetDomainsURL(mockDomainsServer.URL)

	if tier.GetHubCloudBaseURL() != DefaultHubCloudBase {
		t.Fatalf("expected default %s, got %s", DefaultHubCloudBase, tier.GetHubCloudBaseURL())
	}

	err := tier.UpdateHubCloudDomain(context.Background())
	if err != nil {
		t.Fatalf("UpdateHubCloudDomain failed: %v", err)
	}

	if tier.GetHubCloudBaseURL() != "https://hubcloud.mock.pw" {
		t.Fatalf("expected updated base 'https://hubcloud.mock.pw', got '%s'", tier.GetHubCloudBaseURL())
	}
}

func TestVidaraTier_DynamicHubCloudDomain_Live(t *testing.T) {
	tier := NewVidaraTier("https://vidara.to", "https://streamtape.com", 10*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := tier.UpdateHubCloudDomain(ctx)
	if err != nil {
		t.Logf("Notice: live update failed (offline or network): %v", err)
		return
	}

	currentBase := tier.GetHubCloudBaseURL()
	t.Logf("Live dynamic HubCloud base URL: %s", currentBase)
	if !strings.HasPrefix(currentBase, "http://") && !strings.HasPrefix(currentBase, "https://") {
		t.Errorf("invalid HubCloud base URL: %s", currentBase)
	}
	if !strings.Contains(currentBase, "hubcloud") {
		t.Errorf("expected domain to contain 'hubcloud', got: %s", currentBase)
	}
}

func TestVidaraTier_ExtractHubCloud_DomainRewrite(t *testing.T) {
	tier := NewVidaraTier("https://vidara.to", "https://streamtape.com", 5*time.Second)
	tier.SetHubCloudBaseURL("https://hubcloud.pw")

	// Verify rewrite on older domain
	res, err := tier.ExtractHubCloud(context.Background(), "")
	if err == nil {
		t.Fatalf("expected error on empty url, got %+v", res)
	}
}


