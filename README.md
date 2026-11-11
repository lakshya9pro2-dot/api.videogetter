# Vidara 4-Tier Multi-Source Movie/TV Streaming API (Go)

A high-performance backend API written in Go that resolves standard **TMDB IDs** (for both Movies and TV Shows) into signed, playable streaming URLs across a resilient **4-tier fallback architecture** with integrated edge proxying for web browsers and native players.

This project converts the legacy Python implementation into idiomatic, concurrent Go with **zero third-party dependencies**, providing high throughput, sub-second responses, same-IP stream extraction, and robust error recovery.

---

## Table of Contents
1. [Architecture & Fallback Order](#1-architecture--fallback-order)
2. [Proxy Routing & Web Mode (`?web`)](#2-proxy-routing--web-mode-web)
   - [Proxy Routing Matrix](#proxy-routing-matrix)
   - [In-Worker Extraction (StreamHG & Streamtape)](#in-worker-extraction-streamhg--streamtape)
   - [Direct MP4 Zero-Proxy Rule](#direct-mp4-zero-proxy-rule)
3. [What Works & Why (Tier-by-Tier)](#3-what-works--why-tier-by-tier)
   - [Tier 1: Vidara, StreamHG & Streamtape](#tier-1-vidara-streamhg--streamtape)
   - [Tier 2: VidFast Extractor API](#tier-2-vidfast-extractor-api)
   - [Tier 3: Vidrift Multi-Provider Engine](#tier-3-vidrift-multi-provider-engine)
   - [Tier 4: ShowBox / FilmU Scraper](#tier-4-showbox--filmu-scraper)
4. [Security & Authentication Layer](#4-security--authentication-layer)
5. [Timeout & Immediate Cascading Logic](#5-timeout--immediate-cascading-logic)
6. [Quickstart & Deployment](#6-quickstart--deployment)
7. [API Documentation & Examples](#7-api-documentation--examples)
8. [Playback Headers (`Referer` & `Origin`)](#8-playback-headers-referer--origin)
9. [Configuration & Environment Variables](#9-configuration--environment-variables)
10. [Codebase Guide for Developers](#10-codebase-guide-for-developers)
11. [Testing & Verification](#11-testing--verification)

---

## 1. Architecture & Fallback Order

Clients query standard TMDB IDs (e.g., `550` for Fight Club, or `1396/1/1` for Breaking Bad S01E01). The central orchestrator queries tiers in strict priority, followed by the intelligent proxy router:

```text
Incoming Client Request (/api/movie/{tmdb_id} or /api/tv/{tmdb_id}/{season}/{episode})
   │
   ▼
Tier 1: Vidara, StreamHG & Streamtape
   │
   ├── Found in local index & resolved? ──► [Go to Proxy Routing]
   │
   └── Not found / unavailable
          │
          ▼
      Tier 2: VidFast Extractor (vidfast.vc)
          │
          ├── Stream detected in ≤10s? ──────► [Go to Proxy Routing]
          │
          └── Failed / timeout / not found
                 │  (Cascades IMMEDIATELY with 0ms delay)
                 ▼
             Tier 3: Vidrift Engine (embed.vidrift.in)
                 │
                 ├── 1. Direct evionUrl found in HTML? ──► [Go to Proxy Routing]
                 ├── 2. Provider "atlas" returns stream? ──► [Go to Proxy Routing]
                 ├── 3. Provider "vela" returns stream? ───► [Go to Proxy Routing]
                 ├── 4. Provider "orion" returns stream? ──► [Go to Proxy Routing]
                 │
                 └── All Vidrift providers failed
                        │
                        ▼
                    Tier 4: ShowBox / FilmU (box.filmu.in)
                        │
                        ├── Found in Febbox CDN? ─────────────► [Go to Proxy Routing]
                        │
                        └── Not found in any tier
                               │
                               ▼
                          Return Clean HTTP 404 {"success": false, "error": "source_not_found"}
```

```text
Resolved Stream Result
   │
   ▼
Proxy Routing Stage (handlers.proxyResolvedResult)
   │
   ├── Direct MP4 (direct play URL / mp4 container)?
   │     └──► NEVER PROXIED in BOTH Normal and Web (?web) mode.
   │
   ├── Streamtape (st) OR StreamHG / Hanerix (sh / sg)?
   │     └──► Strictly proxied via Netlify Worker (kineflexvideoapi.netlify.app)
   │          in BOTH Normal and Web (?web) mode via In-Worker Extraction.
   │
   └── All Other Sources (Vidara, HubCloud, Tiers 2-4)?
         ├── Normal Mode (!isWeb) ──► Direct CDN stream URL (no proxy).
         └── Web Mode (?web)     ──► Proxied via Cloudflare Worker (proxyservr.../api/hide).
```

### Strict Short-Circuit Rules
- **Tier 1 succeeds** $\rightarrow$ Tiers 2, 3, and 4 are **never called**.
- **Tier 2 succeeds** $\rightarrow$ Tiers 3 and 4 are **never called**.
- **Tier 3 succeeds** $\rightarrow$ Tier 4 is **never called**.
- **Tier Isolation**: Failures, timeouts, or network errors in any tier are trapped safely and cascade to the next tier without stalling the request.

---

## 2. Proxy Routing & Web Mode (`?web`)

Browsers cannot inject custom HTTP `Origin` or `Referer` headers when playing media inside HTML5 `<video>` or `hls.js`. Native players (ExoPlayer, VLC, Kodi), on the other hand, can send headers directly without proxy overhead.

The API provides two operational modes via the `?web` query flag (e.g. `?web=true` or `?web`):

### Proxy Routing Matrix

| Source / Provider | Normal Mode (`/api/movie/{id}`) | Web Mode (`/api/movie/{id}?web`) | Proxy Infrastructure |
| :--- | :--- | :--- | :--- |
| **Direct MP4 (`direct` / `play`)** | **Direct** (Zero Proxy) | **Direct** (Zero Proxy) | *Bypasses all proxies* |
| **StreamHG / Hanerix (`sh` / `sg`)** | **Proxied** (In-Worker Extraction) | **Proxied** (In-Worker Extraction) | Netlify Serverless (`kineflexvideoapi.netlify.app`) |
| **Streamtape (`st`)** | **Proxied** (In-Worker Extraction) | **Proxied** (In-Worker Extraction) | Netlify Serverless (`kineflexvideoapi.netlify.app`) |
| **Vidara (`va`)** | **Direct** (CDN with headers) | **Proxied** (`/api/hide`) | Cloudflare Worker (`proxyservr.kineflex-netflex.workers.dev`) |
| **HubCloud (`hdcloud` / `hubcloud`)** | **Direct** (CDN with headers) | **Proxied** (`/api/hide`) | Cloudflare Worker (`proxyservr.kineflex-netflex.workers.dev`) |
| **Tier 2 VidFast** | **Direct** (HLS stream) | **Proxied** (`/api/hide`) | Cloudflare Worker (`proxyservr.kineflex-netflex.workers.dev`) |
| **Tier 3 Vidrift (Evion/Atlas/Vela)** | **Direct** (HLS stream) | **Proxied** (`/api/hide`) | Cloudflare Worker (`proxyservr.kineflex-netflex.workers.dev`) |
| **Tier 4 ShowBox (Febbox)** | **Direct** (HLS stream) | **Proxied** (`/api/hide`) | Cloudflare Worker (`proxyservr.kineflex-netflex.workers.dev`) |

---

### In-Worker Extraction (StreamHG & Streamtape)

#### 1. StreamHG / Hanerix (`sh` / `sg`)
- **The Challenge (Same-IP Token Binding):** Hanerix embeds (`hanerix.com/e/{id}`) generate dynamic tokens inside packed JavaScript (`eval(function(p,a,c,k,e,d)...)`). The resulting playlist URL (`https://.../stream/<token>/.../master.m3u8`) is strictly bound to the **IP address** that fetched the HTML embed. If the Go backend extracted the stream and passed the URL to a separate proxy or client, upstream servers returned `HTTP 403 Forbidden`.
- **The Solution:** The Go backend transmits only the `sh` ID to the Netlify worker via `workerClient.ExtractSession(ctx, "streamhg", id)`. The worker:
  1. Fetches the embed directly on its own serverless edge IP (with fallbacks to `hanerix.com`, `streamhg.com`, `hglink.to`, `hgcloud.to`).
  2. De-obfuscates the Dean Edwards packed JS and extracts `links.hls4` / `links.hls3` / `links.hls2`.
  3. Establishes a stateless HMAC session and rewrites the playlist and segments.
  4. Returns the proxied playback URL. Because extraction and proxying share the exact same edge IP, playback never encounters HTTP 403.

#### 2. Streamtape (`st`)
- **The Challenge:** Streamtape actively detects and blocks Cloudflare ASN `13335` IP ranges and enforces strict single-client token validation.
- **The Solution:** Proxied strictly through the Netlify serverless Go function running on AWS Lambda IP space outside Cloudflare. Handles `Range: bytes=start-end` requests and forwards `HTTP 206 Partial Content` for instant seeking.

---

### Direct MP4 Zero-Proxy Rule

When a source is identified as Direct MP4 (e.g. `play: "https://cdn.videy.co/gzX5L28G1.mp4"` or `stream_type: "mp4"` with direct accessibility):
- **Never Proxied:** Bypasses both the Netlify worker and Cloudflare `proxyservr` in **both** normal mode and web mode.
- **No Overhead:** Eliminates proxy latency, avoids unnecessary worker invocations, and protects private network interfaces from SSRF filters.

---

## 3. What Works & Why (Tier-by-Tier)

### Tier 1: Vidara, StreamHG & Streamtape
- **What Works:**
  - **In-Memory Store:** Loads remote JSON dataset (`https://www.jsonkeeper.com/b/FWSAK`) and local fallback files into concurrent-safe in-memory maps (`movieIndex` and `tvIndex`). Lookup is $O(1)$ and takes **< 1 millisecond**.
  - **Dual Upstream Handlers:** Supports Vidara (`va`), Streamtape (`st`), and StreamHG / Hanerix (`sh` and `sg`) video IDs.
  - **Automated Delegation:** If `sh` or `st` is encountered, delegates directly to worker edge extraction for 100% reliable stream delivery.
  - **Mirror Resiliency:** Automatically traverses mirror domains for StreamHG (`hanerix.com`, `streamhg.com`, `hglink.to`, `hgcloud.to`).
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
  - VidFast either detects a stream within 4–7 seconds or does not have it. Bounding Tier 2 prevents slow or hanging extractors from starving the request context.

---

### Tier 3: Vidrift Multi-Provider Engine
- **What Works:**
  - **Embed Metadata Extraction:** Fetches `https://embed.vidrift.in/embed/movie/{id}` or `https://embed.vidrift.in/embed/tv/{id}/{s}/{e}` with mobile browser headers.
  - **Priority 1 — Direct `evionUrl` (Fastest & Highest Quality):**
    - Parses `embedMeta.evionUrl` directly embedded in HTML.
    - Resolves in **< 1 second** without making any secondary API calls.
    - Yields a Master HLS playlist (`1080p`) with multi-audio tracks (9 languages) and embedded subtitles.
  - **Priority 2 — Fallback Provider `atlas`:** Calls `embed.vidrift.in/api/source/...` for MPEG-TS streams.
  - **Priority 3 — Fallback Provider `vela`:** High-quality fMP4 HLS streams.
  - **Priority 4 — Fallback Provider `orion`:** Secondary fallback provider.
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

## 4. Security & Authentication Layer

The API is protected by [`handlers.OriginFilterMiddleware`](file:///home/batz/Desktop/working%20web/api.videogetter-main/api.videogetter-main/handlers/middleware.go):

| Route / Traffic Type | Access Rule | Behavior |
|---|---|---|
| `/health` | **Public** | Always returns HTTP 200 `{"status": "ok"}`. |
| **M3U Streams** (`.m3u`, `.m3u8`, `?format=m3u`) | **Public** | Accessible to all external players (ExoPlayer, VLC, Kodi, IPTV apps) without Origin or API key checks. |
| **CORS Preflight** (`OPTIONS`) | **Allowed Origin** | Responds with standard CORS headers (`Access-Control-Allow-Origin: *` or allowed origin). |
| **JSON API** (`/api/movie/*`, `/api/tv/*`) | **Authenticated** | Requires `Origin: https://api.kineflex.site` **AND** `X-Api-Key: lakshya9310081539@`. |
| **Unauthorized Visitors** | **Protected** | Browsers/scrapers attempting direct access are HTTP 302 redirected to `https://api.kineflex.site/`. |

---

## 5. Timeout & Immediate Cascading Logic

1. **Sub-Context Timeout Isolation:** Tier 2 wraps its work in a dedicated sub-context (`tierBudget := time.Duration(t.GetTimeoutSeconds()+3) * time.Second`).
2. **Immediate Cascading (0ms delay):** The moment Tier 2 fails or times out, it releases control and returns `(nil, nil)`. The central resolver immediately invokes Tier 3.
3. **Parent Context Headroom:** The parent request context timeout is set to **50 seconds**, ensuring Tiers 3 and 4 have sufficient execution time.
4. **No Redundant Retries on 404:** HTTP 404 is treated as a clean failure rather than a network error, eliminating wasteful second attempts.

---

## 6. Quickstart & Deployment

### Prerequisites
- **Go 1.22+** (tested on Go 1.26)

### Build Native Binary
```bash
go build -o vidara-server .
```

### Run
```bash
# Start server on default port 8080
./vidara-server

# Or customize environment variables
PORT=8080 VIDFAST_TIMEOUT_SECONDS=10 ./vidara-server
```

---

## 7. API Documentation & Examples

### 1. Movie Playback (Normal Mode)
```bash
curl -s "http://localhost:8080/api/movie/550" \
  -H "Origin: https://api.kineflex.site" \
  -H "X-Api-Key: lakshya9310081539@"
```
**Response (Direct CDN Stream with Headers):**
```json
{
  "success": true,
  "tmdb_id": 550,
  "type": "movie",
  "tier": 3,
  "source": "vidrift-atlas",
  "title": "Fight Club",
  "direct": true,
  "url": "https://edge.reelvault.click/proxy?u=scddAAVILopoYtGpgZVFi7rSy3t0Su4mM0TfST2FXlPF...",
  "headers": {
    "Origin": "https://embed.vidrift.in",
    "Referer": "https://embed.vidrift.in/"
  }
}
```

---

### 2. Movie Playback (Web Mode `?web`)
```bash
curl -s "http://localhost:8080/api/movie/550?web" \
  -H "Origin: https://api.kineflex.site" \
  -H "X-Api-Key: lakshya9310081539@"
```
**Response (Proxied via Cloudflare `proxyservr`, Headers Cleared for Browser):**
```json
{
  "success": true,
  "tmdb_id": 550,
  "type": "movie",
  "tier": 3,
  "source": "vidrift-atlas",
  "title": "Fight Club",
  "direct": false,
  "url": "https://proxyservr.kineflex-netflex.workers.dev/api/hide/aHR0cHM6Ly9lZGdlLnJlZWx2YXVsdC5jbGljay9wcm94eT91PXNjZGRBQVZJTG9wb1l0R3BnWlZGaTdyU3kzdDBTdTQtTTBUZlNUMkZYbFBG...",
  "headers": {}
}
```

---

### 3. StreamHG Stream (Both Normal & Web Mode)
Resolving a title with a StreamHG / Hanerix source (e.g. `sh` / `sg` ID `lt1t142hhe8p`):
```bash
curl -s "http://localhost:8080/api/movie/1234139" \
  -H "Origin: https://api.kineflex.site" \
  -H "X-Api-Key: lakshya9310081539@"
```
**Response (In-Worker Extracted & Proxied via Netlify Worker):**
```json
{
  "success": true,
  "tmdb_id": 1234139,
  "type": "movie",
  "tier": 1,
  "source": "streamhg",
  "direct": false,
  "stream_type": "hls",
  "url": "https://kineflexvideoapi.netlify.app/session/d73489fe0b3294c.../master.m3u8",
  "headers": {}
}
```

---

### 4. Direct MP4 (Both Normal & Web Mode)
Resolving a title with Direct MP4 playback (e.g. Videy / direct CDN):
```bash
curl -s "http://localhost:8080/api/movie/999999?web" \
  -H "Origin: https://api.kineflex.site" \
  -H "X-Api-Key: lakshya9310081539@"
```
**Response (Never Proxied — Pure Direct MP4):**
```json
{
  "success": true,
  "tmdb_id": 999999,
  "type": "movie",
  "tier": 1,
  "source": "direct",
  "direct": true,
  "stream_type": "mp4",
  "url": "https://cdn.videy.co/gzX5L28G1.mp4",
  "headers": {}
}
```

---

### 5. TV Show Playback
```bash
curl -s "http://localhost:8080/api/tv/1396/1/1?web" \
  -H "Origin: https://api.kineflex.site" \
  -H "X-Api-Key: lakshya9310081539@"
```

---

### 6. Extended M3U Playlist (Public / No Auth Required)
```bash
curl -s "http://localhost:8080/api/movie/550?format=m3u"
```
**Response (`application/vnd.apple.mpegurl`):**
```m3u
#EXTM3U

#EXTINF:-1,Fight Club
#EXTVLCOPT:http-referrer=https://embed.vidrift.in/
#EXTVLCOPT:http-origin=https://embed.vidrift.in
https://edge.reelvault.click/proxy?u=scddAAVILopoYtGpgZVFi7rSy3t0Su4mM0TfST2FXlPF...
```

---

## 8. Playback Headers (`Referer` & `Origin`)

When streaming directly in native players (Normal Mode), these upstream headers are supplied in `result.headers`:

| Tier | Source | `Referer` | `Origin` | Handled By |
|---|---|---|---|---|
| **Tier 1** | Vidara | `https://vidara.to/` | `https://vidara.to` | Native Player or `proxyservr` |
| **Tier 1** | StreamHG | `https://hanerix.com/` | `https://hanerix.com` | Handled Server-Side by Netlify Worker |
| **Tier 1** | Streamtape | `https://streamtape.com/` | `https://streamtape.com` | Handled Server-Side by Netlify Worker |
| **Tier 2** | VidFast Extractor | `https://vidfast.vc/` | `https://vidfast.vc` | Native Player or `proxyservr` |
| **Tier 3** | Vidrift | `https://embed.vidrift.in/` | `https://embed.vidrift.in` | Native Player or `proxyservr` |
| **Tier 4** | ShowBox / Febbox | `https://www.febbox.com/` | `https://www.febbox.com` | Native Player or `proxyservr` |

> [!NOTE]
> When a stream is proxied (e.g. StreamHG, Streamtape, or any source in `?web` mode), `result.headers` is intentionally cleared because the proxy worker injects the required headers upstream on the server side.

---

## 9. Configuration & Environment Variables

| Variable | Default | Purpose |
|---|---|---|
| `PORT` | `8080` | Port for the HTTP server to listen on. |
| `ALLOWED_ORIGIN` | `https://api.kineflex.site` | Strictly allowed Origin for JSON API access. |
| `API_KEY` | `lakshya9310081539@` | Secret API key required by middleware (`X-Api-Key`). |
| `REDIRECT_URL` | `https://api.kineflex.site/` | Redirection destination for unauthorized web traffic. |
| `PROXY_WORKER_URL` | `https://kineflexvideoapi.netlify.app` | Primary Netlify serverless proxy for Streamtape and StreamHG in-worker extraction. |
| `HUBCLOUD_PROXY_URL` | `https://proxyservr.kineflex-netflex.workers.dev` | Dedicated Cloudflare Worker for HubCloud & Web mode (`?web`) stream proxying. |
| `APP_URL_WORKER` | `https://pinggy-registry.kineflex-netflex.workers.dev/api/app` | Cloudflare Worker returning active Pinggy extractor URL. |
| `VIDFAST_BASE_URL` | `https://vidfast.vc` | Base URL prefix for VidFast extraction targets. |
| `VIDFAST_TIMEOUT_SECONDS` | `10` | Extractor timeout query parameter (fast-fail threshold). |
| `VIDRIFT_BASE_URL` | `https://embed.vidrift.in` | Base URL for Tier 3 Vidrift embed service. |
| `SHOWBOX_BASE_URL` | `https://box.filmu.in` | Base URL for Tier 4 ShowBox scraper. |
| `SHOWBOX_API_KEY` | `09eb4299...` | ShowBox scraping API key. |
| `FEBBOX_TOKEN` | `eyJ0eXAi...` | Febbox user JWT authentication token. |
| `TMDB_KEY` | `e6333b32...` | TMDB API key for metadata resolution. |
| `REQUEST_TIMEOUT_SECONDS` | `10` | Default timeout for standard HTTP operations. |
| `VIDARA_JSON_URL` | `https://www.jsonkeeper.com/b/FWSAK` | Remote dataset URL for Tier 1 store. |

---

## 10. Codebase Guide for Developers

```text
.
├── config.go                     # Configuration loader and environment bindings
├── main.go                       # Server initialization, tier setup, and route binding
├── models/
│   └── models.go                 # Shared structs: ResolutionResult, Playback (with sg/sh mapping)
├── handlers/
│   ├── api.go                    # Route handlers (/api/movie, /api/tv, proxyResolvedResult)
│   ├── middleware.go             # Origin, Referer, and API Key security filter
│   ├── api_test.go               # HTTP integration tests
│   └── web_proxy_test.go         # Tests for ?web mode, Direct MP4, Streamtape & StreamHG
├── proxy/
│   └── client.go                 # Multi-worker proxy client (ExtractSession & CreateSession)
├── resolver/
│   ├── resolver.go               # Central 4-tier orchestrator (Tier 1 -> 2 -> 3 -> 4)
│   └── resolver_test.go          # Cascade priority and fallback unit tests
├── tiers/
│   ├── tier1_vidara.go           # Tier 1: In-memory store, Hanerix JS unpacking & worker delegation
│   ├── tier2_extractor.go        # Tier 2: VidFast Pinggy client with sub-context bounds
│   ├── tier3_vidrift.go          # Tier 3: Vidrift Engine (Evion -> Atlas -> Vela -> Orion)
│   └── tier4_showbox.go          # Tier 4: ShowBox Febbox HLS scraper & M3U generator
├── netlify-proxy-go/             # Netlify serverless Go proxy function
│   ├── netlify/functions/proxy/  # Serverless entrypoint (AWS Lambda Go runtime)
│   └── pkg/proxy/                # Streamtape & StreamHG extractors, Range proxy, HLS rewriter
└── worker/                       # Cloudflare Worker HLS/MP4 proxy implementation
```

---

## 11. Testing & Verification

Run the entire test suite across the main repository and the Netlify proxy:

```bash
# Run backend test suite
go test -v ./...

# Run Netlify serverless proxy test suite
cd netlify-proxy-go && go test -v ./...
```

### Verified Test Suites
1. **Fallback Priority** (`resolver`):
   - Proves Tier 1 success short-circuits Tiers 2, 3, and 4.
   - Proves Tier 2 success short-circuits Tiers 3 and 4.
   - Proves clean 404 `source_not_found` when all tiers fail.
2. **Web Proxy & Routing Rules** (`handlers`):
   - **StreamHG & Streamtape:** Verified strictly routed through `kineflexvideoapi.netlify.app` in **both** normal mode and web mode.
   - **Direct MP4:** Verified that direct MP4 streams (`play` / `direct`) are **never proxied** in either normal mode or web mode.
   - **Web Mode (`?web`):** Verified that Vidara, HubCloud, and Tiers 2–4 are proxied via `proxyservr.kineflex-netflex.workers.dev/api/hide`.
3. **StreamHG & Streamtape In-Worker Extraction** (`netlify-proxy-go`):
   - Unpacking Dean Edwards `p,a,c,k,e,d` JavaScript on serverless edge.
   - Streamtape `HTTP 206 Partial Content` Range forwarding.
4. **Security & M3U Filter** (`handlers`):
   - Confirms that `/health` and `.m3u` endpoints are public.
   - Confirms that protected JSON endpoints reject unauthorized requests with HTTP 302 redirects.
