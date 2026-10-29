# Vidara 4-Tier Multi-Source Movie/TV Streaming API (Go)

A high-performance backend API written in Go that resolves standard **TMDB IDs** (for both Movies and TV Shows) into signed, playable streaming URLs across a resilient **4-tier fallback architecture**.

This project converts the legacy Python implementation into idiomatic, concurrent Go with **zero third-party dependencies**, providing high throughput, sub-second responses, and robust error recovery.

---

## Table of Contents
1. [Architecture & Fallback Order](#1-architecture--fallback-order)
2. [What Works & Why (Tier-by-Tier)](#2-what-works--why-tier-by-tier)
   - [Tier 1: Vidara & StreamHG](#tier-1-vidara--streamhg)
   - [Tier 2: VidFast Extractor API](#tier-2-vidfast-extractor-api)
   - [Tier 3: Vidrift Multi-Provider Engine](#tier-3-vidrift-multi-provider-engine)
   - [Tier 4: ShowBox / FilmU Scraper](#tier-4-showbox--filmu-scraper)
3. [Security & Authentication Layer](#3-security--authentication-layer)
4. [Timeout & Immediate Cascading Logic](#4-timeout--immediate-cascading-logic)
5. [Quickstart & Deployment](#5-quickstart--deployment)
6. [API Documentation & Examples](#6-api-documentation--examples)
7. [Playback Headers (`Referer` & `Origin`)](#7-playback-headers-referer--origin)
8. [Configuration & Environment Variables](#8-configuration--environment-variables)
9. [Codebase Guide for Developers](#9-codebase-guide-for-developers)
10. [Testing & Verification](#10-testing--verification)

---

## 1. Architecture & Fallback Order

Clients query standard TMDB IDs (e.g., `550` for Fight Club, or `1396/1/1` for Breaking Bad S01E01). The central orchestrator queries tiers in strict priority:

```text
Incoming Client Request (/api/movie/{tmdb_id} or /api/tv/{tmdb_id}/{season}/{episode})
   │
   ▼
Tier 1: Vidara & StreamHG
   │
   ├── Found in local index & resolved? ──► Return HTTP 200 (tier: 1, source: "vidara" | "streamhg")
   │
   └── Not found / unavailable
          │
          ▼
      Tier 2: VidFast Extractor (vidfast.vc)
          │
          ├── Stream detected in ≤10s? ──────► Return HTTP 200 (tier: 2, source: "vidfast-extractor")
          │
          └── Failed / timeout / not found
                 │  (Cascades IMMEDIATELY with 0ms delay)
                 ▼
             Tier 3: Vidrift Engine (embed.vidrift.in)
                 │
                 ├── 1. Direct evionUrl found in HTML? ──► Return HTTP 200 (tier: 3, source: "vidrift-evion")
                 ├── 2. Provider "atlas" returns stream? ──► Return HTTP 200 (tier: 3, source: "vidrift-atlas")
                 ├── 3. Provider "vela" returns stream? ───► Return HTTP 200 (tier: 3, source: "vidrift-vela")
                 ├── 4. Provider "orion" returns stream? ──► Return HTTP 200 (tier: 3, source: "vidrift-orion")
                 │
                 └── All Vidrift providers failed
                        │
                        ▼
                    Tier 4: ShowBox / FilmU (box.filmu.in)
                        │
                        ├── Found in Febbox CDN? ─────────────► Return HTTP 200 (tier: 4, source: "showbox")
                        │
                        └── Not found in any tier
                               │
                               ▼
                          Return Clean HTTP 404 {"success": false, "error": "source_not_found"}
```

### Strict Short-Circuit Rules
- **Tier 1 succeeds** $\rightarrow$ Tiers 2, 3, and 4 are **never called**.
- **Tier 2 succeeds** $\rightarrow$ Tiers 3 and 4 are **never called**.
- **Tier 3 succeeds** $\rightarrow$ Tier 4 is **never called**.
- **Tier Isolation**: Failures, timeouts, or network errors in any tier are trapped safely and cascade to the next tier without stalling the request.

---

## 2. What Works & Why (Tier-by-Tier)

### Tier 1: Vidara & StreamHG
- **What Works:**
  - **In-Memory Store:** Loads remote JSON dataset (`https://www.jsonkeeper.com/b/FWSAK`) and local fallback files into concurrent-safe in-memory maps (`movieIndex` and `tvIndex`). Lookup is $O(1)$ and takes **< 1 millisecond**.
  - **Dual Upstream Handlers:** Supports both Vidara (`va`) and Streamtape (`st`) video IDs with automatic token evaluation.
  - **StreamHG Integration (`sh`):** Resolves StreamHG / Hanerix directly (`https://hanerix.com/e/{id}`), unpacks Dean Edwards `p,a,c,k,e,d` JavaScript shells, extracts `links.hls4` / `hls3` / `hls2` master HLS streams with multi-audio, and handles playback headers (`Referer: https://hanerix.com/e/{id}`).
- **Why It's Built This Way:**
  - Provides instantaneous responses for high-demand, pre-curated content without hitting external scrapers or consuming remote rate limits.

---

### Tier 2: VidFast Extractor API
- **What Works:**
  - **Dynamic Tunnel Discovery:** Queries the Cloudflare Worker API (`APP_URL_WORKER`) to discover active dynamic Pinggy extractor URLs.
  - **Thread-Safe Caching:** Caches the extractor URL with automatic expiration checks and a 5-minute safety threshold.
  - **Dedicated Per-Tier Timeout Bounding:** Enforces a strict sub-context budget (`VIDFAST_TIMEOUT_SECONDS`, default 10s). If VidFast does not detect an HLS stream within this window, it cancels Tier 2 cleanly and yields control to Tier 3.
  - **Optimized Network Retry:** Server 5xx/Bad Gateway errors trigger a single URL refresh retry. HTTP 404 ("Not Found") does **not** retry, cascading to Tier 3 immediately.
- **Why It's Built This Way:**
  - VidFast either detects a stream within 4–7 seconds or does not have it. Bounding Tier 2 prevents slow or hanging extractors from starving the request context, ensuring that missing sources immediately cascade to Tier 3.

---

### Tier 3: Vidrift Multi-Provider Engine
- **What Works:**
  - **Embed Metadata Extraction:** Fetches `https://embed.vidrift.in/embed/movie/{id}` or `https://embed.vidrift.in/embed/tv/{id}/{s}/{e}` with mobile browser headers.
  - **Priority 1 — Direct `evionUrl` (Fastest & Highest Quality):**
    - Parses `embedMeta.evionUrl` directly embedded in HTML.
    - Resolves in **< 1 second** without making any secondary API calls.
    - Yields a Master HLS playlist (`1080p`) with multi-audio tracks (9 languages) and embedded subtitles.
  - **Priority 2 — Fallback Provider `atlas`:**
    - Calls `https://embed.vidrift.in/api/source/{type}/{id}...?provider=atlas` using session token.
    - Yields multi-bitrate MPEG-TS HLS streams.
  - **Priority 3 — Fallback Provider `vela`:**
    - High-quality fMP4 HLS streams.
  - **Priority 4 — Fallback Provider `orion`:**
    - Additional fallback provider.
  - **Extensible Architecture:** Providers are defined in a clean slice (`VidriftProviderList`). Adding new providers (e.g. `nova`, `lyra`) requires just one line of code.
- **Why It's Built This Way:**
  - `evionUrl` provides the best user experience with zero API latency. The multi-provider fallback guarantees high resilience when specific upstream CDNs are under load.

---

### Tier 4: ShowBox / FilmU Scraper
- **What Works:**
  - **Direct Scraper:** Queries `box.filmu.in` with API keys and Febbox JWT tokens.
  - **Strict M3U8 Filtering:** Excludes raw video containers (`.mkv`, `.mp4`) and extracts pure `.m3u8` streams.
  - **Stream Resolution Ranking:** Prioritizes streams by resolution ($4\text{K} > 1080\text{p} > 720\text{p} > 360\text{p}$) and file size.
  - **Universal Extended M3U Generator:** Generates compliant playlists with player directives (`#EXTVLCOPT`, `#EXTHTTP`, `#KODIPROP`) accessible via `?format=m3u` or `/playlist.m3u`.
- **Why It's Built This Way:**
  - Acts as the deep catalogue fallback for obscure titles not present in Tiers 1–3, and powers IPTV / external media players.

---

## 3. Security & Authentication Layer

The API is protected by [`handlers.OriginFilterMiddleware`](file:///home/batz/Downloads/api.videogetter-main/api.videogetter-main/handlers/middleware.go#L121):

| Route / Traffic Type | Access Rule | Behavior |
|---|---|---|
| `/health` | **Public** | Always returns HTTP 200 `{"status": "ok"}`. |
| **M3U Streams** (`.m3u`, `.m3u8`, `?format=m3u`) | **Public** | Fully accessible to all video players (ExoPlayer, VLC, Kodi, IPTV apps) without Origin or API key checks. |
| **CORS Preflight** (`OPTIONS`) | **Allowed Origin** | Responds with standard CORS headers for `https://api.kineflex.site`. |
| **JSON API** (`/api/movie/*`, `/api/tv/*`) | **Authenticated** | Requires `Origin: https://api.kineflex.site` **AND** `X-Api-Key: lakshya9310081539@`. |
| **Unauthorized Visitors** | **Protected** | Browsers/scrapers attempting direct access are HTTP 302 redirected to `https://api.kineflex.site/`. |

---

## 4. Timeout & Immediate Cascading Logic

### The Problem Solved
Previously, Tier 2 used a hardcoded 20s extractor timeout and 25s client timeout. If a source was missing or a tunnel glitched, Tier 2 retried and consumed 40+ seconds. Because the handler request context expired at 35 seconds, Tier 3 never received a chance to run.

### The Solution Implemented
1. **Sub-Context Timeout Isolation:** Tier 2 wraps its work in a dedicated sub-context (`tierBudget := time.Duration(t.GetTimeoutSeconds()+3) * time.Second`).
2. **Immediate Cascading (0ms delay):** The moment Tier 2 fails or times out, it releases control and returns `(nil, nil)`. The central resolver immediately invokes Tier 3.
3. **Parent Context Headroom:** The parent request context timeout is set to **50 seconds**, ensuring Tiers 3 and 4 have plenty of time.
4. **No Redundant Retries on 404:** HTTP 404 is treated as a clean failure rather than a network error, eliminating wasteful second attempts.

### Benchmark Results
- **When Tier 2 is Offline / Unreachable:** Cascades to Tier 3 in **< 1 millisecond**; resolves Breaking Bad in **0.95 seconds** total.
- **When Video is Missing on Tier 2:** Tier 2 fast-fails in ~7 seconds; resolves on Tier 3 in **8.8 seconds** total.
- **When Video Exists on Tier 2:** Resolves via Tier 2 in **4.9 seconds**.

---

## 5. Quickstart & Deployment

### Prerequisites
- **Go 1.22+** (tested on Go 1.26.0)

### Build Native Binary
```bash
go build -o vidara-server .
```

### Run
```bash
# Start server on default port 8080
./vidara-server

# Or customize environment variables
PORT=8080 VIDFAST_TIMEOUT_SECONDS=8 ./vidara-server
```

---

## 6. API Documentation & Examples

### 1. Health Check
```bash
curl -i http://localhost:8080/health
```
```json
HTTP/1.1 200 OK
{"status":"ok"}
```

---

### 2. TV Episode Resolution
```http
GET /api/tv/{tmdb_id}/{season}/{episode}
```

```bash
curl -s "http://localhost:8080/api/tv/1396/1/1" \
  -H "Origin: https://api.kineflex.site" \
  -H "X-Api-Key: lakshya9310081539@"
```

**Response (HTTP 200 — Tier 3 Evion):**
```json
{
  "success": true,
  "tmdb_id": 1396,
  "type": "tv",
  "tier": 3,
  "source": "vidrift-evion",
  "title": "Breaking Bad",
  "season": 1,
  "episode": 1,
  "url": "https://img.animecurx.tech/tvshows/afe8c1475da4885935a8/index.m3u8",
  "headers": {
    "Origin": "https://embed.vidrift.in",
    "Referer": "https://embed.vidrift.in/"
  }
}
```

---

### 3. Movie Resolution
```http
GET /api/movie/{tmdb_id}
```

```bash
curl -s "http://localhost:8080/api/movie/550" \
  -H "Origin: https://api.kineflex.site" \
  -H "X-Api-Key: lakshya9310081539@"
```

**Response (HTTP 200 — Tier 3 Atlas):**
```json
{
  "success": true,
  "tmdb_id": 550,
  "type": "movie",
  "tier": 3,
  "source": "vidrift-atlas",
  "title": "Fight Club",
  "url": "https://edge.reelvault.click/proxy?u=scddAAVILopoYtGpgZVFi7rSy3t0Su4mM0TfST2FXlPF...",
  "headers": {
    "Origin": "https://embed.vidrift.in",
    "Referer": "https://embed.vidrift.in/"
  }
}
```

---

### 4. Direct M3U Playlist (No Auth Required)
External video players, IPTV apps, and VLC can request playlists directly:

```bash
curl -s "http://localhost:8080/api/movie/550?format=m3u"
```

**Response (`application/vnd.apple.mpegurl`):**
```m3u
#EXTM3U

#EXTINF:-1,Fight Club
https://edge.reelvault.click/proxy?u=scddAAVILopoYtGpgZVFi7rSy3t0Su4mM0TfST2FXlPF...
```

---

## 7. Playback Headers (`Referer` & `Origin`)

Every stream returns the headers required by upstream CDNs to prevent HTTP 403 Forbidden:

| Tier | Source | `Referer` | `Origin` |
|---|---|---|---|
| **Tier 1** | Vidara | `https://vidara.to/` | `https://vidara.to` |
| **Tier 1** | StreamHG | `https://hanerix.com/` | `https://hanerix.com` |
| **Tier 2** | VidFast Extractor | `https://vidfast.vc/` | `https://vidfast.vc` |
| **Tier 3** | Vidrift | `https://embed.vidrift.in/` | `https://embed.vidrift.in` |
| **Tier 4** | ShowBox / Febbox | `https://www.febbox.com/` | `https://www.febbox.com` |

---

## 8. Configuration & Environment Variables

| Variable | Default | Purpose |
|---|---|---|
| `PORT` | `8080` | Port for the HTTP server to listen on. |
| `ALLOWED_ORIGIN` | `https://api.kineflex.site` | Strictly allowed origin for JSON API access. |
| `API_KEY` | `lakshya9310081539@` | Secret API key required by middleware. |
| `REDIRECT_URL` | `https://api.kineflex.site/` | Redirection destination for unauthorized web traffic. |
| `APP_URL_WORKER` | `https://pinggy-registry.kineflex-netflex.workers.dev/api/app` | Cloudflare Worker returning active Pinggy extractor URL. |
| `VIDFAST_BASE_URL` | `https://vidfast.vc` | Base URL prefix for VidFast extraction targets. |
| `VIDFAST_TIMEOUT_SECONDS` | `10` | Extractor timeout query parameter (fast-fail threshold). |
| `VIDRIFT_BASE_URL` | `https://embed.vidrift.in` | Base URL for Tier 3 Vidrift embed service. |
| `SHOWBOX_BASE_URL` | `https://box.filmu.in` | Base URL for Tier 4 ShowBox scraper. |
| `SHOWBOX_API_KEY` | `09eb4299...` | ShowBox scraping API key. |
| `FEBBOX_TOKEN` | `eyJ0eXAi...` | Febbox user JWT authentication token. |
| `TMDB_KEY` | `e6333b32...` | TMDB API key for metadata resolution. |
| `REQUEST_TIMEOUT_SECONDS` | `10` | Default timeout for standard HTTP client operations. |
| `VIDARA_JSON_URL` | `https://www.jsonkeeper.com/b/FWSAK` | Remote dataset URL for Tier 1 store. |

---

## 9. Codebase Guide for Developers

```text
.
├── config.go                 # Configuration loader and environment bindings
├── main.go                   # Server initialization, tier setup, and route binding
├── models/
│   └── models.go             # Shared structs: ResolutionResult, Playback, AudioTrack
├── handlers/
│   ├── api.go                # HTTP route handlers (/api/movie, /api/tv, M3U)
│   ├── middleware.go         # Origin, Referer, and API Key security filter
│   └── api_test.go           # End-to-end HTTP integration tests
├── resolver/
│   ├── resolver.go           # Central 4-tier orchestrator (Tier 1 -> 2 -> 3 -> 4)
│   └── resolver_test.go      # Cascade priority and fallback unit tests
└── tiers/
    ├── tier1_vidara.go       # Tier 1: In-memory store, JSON repair, Vidara/StreamHG
    ├── tier2_extractor.go    # Tier 2: VidFast Pinggy client with sub-context bounds
    ├── tier3_vidrift.go      # Tier 3: Vidrift Engine (Evion -> Atlas -> Vela -> Orion)
    ├── tier3_vidrift_test.go # Tier 3 provider fallback unit tests
    └── tier4_showbox.go      # Tier 4: ShowBox Febbox HLS scraper & M3U generator
```

### Key Interfaces
All tiers implement the `TierResolver` interface defined in [`resolver/resolver.go`](file:///home/batz/Downloads/api.videogetter-main/api.videogetter-main/resolver/resolver.go#L11):

```go
type TierResolver interface {
    ResolveMovie(ctx context.Context, tmdbID int) (*models.ResolutionResult, error)
    ResolveTV(ctx context.Context, tmdbID, season, episode int) (*models.ResolutionResult, error)
}
```

---

## 10. Testing & Verification

Run the entire test suite:
```bash
go test -v ./...
```

### Verified Test Cases
1. **Fallback Priority** (`resolver`):
   - Proves Tier 1 success never calls Tiers 2, 3, or 4.
   - Proves Tier 2 success never calls Tiers 3 or 4.
   - Proves Tier 3 executes when Tiers 1 and 2 fail.
   - Proves Tier 4 executes when Tiers 1, 2, and 3 fail.
   - Proves clean 404 `source_not_found` when all tiers fail.
2. **Tier 2 Timeout Isolation** (`tiers`):
   - Confirms that application-level failures (`success: false`) do not retry and cascade cleanly.
   - Confirms that dead worker or extractor tunnels fail fast without hanging the caller.
3. **Tier 3 Provider Hierarchy** (`tiers`):
   - Priority 1: `evionUrl` resolved from HTML metadata.
   - Priority 2: Fallback to `atlas` when `evionUrl` is absent.
   - Priority 3: Fallback to `vela` when `atlas` fails.
   - Extension test: Proves adding a custom provider takes a single slice append.
4. **Security & M3U Filter** (`handlers`):
   - Confirms that `/health` and `.m3u` endpoints are public.
   - Confirms that protected JSON endpoints reject unauthorized requests with HTTP 302 redirects.
