# Cloudflare Worker: HLS Playback Proxy

High-performance HLS playback proxy built on Cloudflare Workers. Handles upstream media fetching, server-side `Origin` / `Referer` headers, master/variant playlist rewriting, segment relaying (`.ts`, `.m4s`, `.mp4`), byte-range streaming, and stateless/distributed session management.

## Architecture

- **No Media Transcoding:** Bitstreams and audio tracks are preserved 100% intact.
- **Playlist Rewriting:** Rewrites master playlists, variant stream URIs, media segments, `#EXT-X-MEDIA` (multi-language audio and subtitle tracks), `#EXT-X-KEY` (encryption keys), and `#EXT-X-MAP` (initialization segments).
- **Security & SSRF Protection:** Validates upstream URLs (strict HTTPS only), blocks loopback/LAN IPv4/IPv6, blocks cloud metadata endpoints (`169.254.169.254`), and supports domain whitelisting.
- **Session Architecture:** Unguessable cryptographically signed session tokens with in-memory caching and optional Cloudflare KV integration. Sessions resolve across any edge location worldwide.
- **Zero Client Configuration:** Clients receive clean standard HTTPS playback URLs (`/session/:id/master.m3u8`) and do NOT need to supply upstream `Origin` or `Referer` headers.

---

## API Contract

### 1. Create Playback Session

- **Endpoint:** `POST /session` (or `POST /api/session`)
- **Headers:**
  - `Content-Type: application/json`
  - `X-Worker-Key: <WORKER_SECRET>` (if secret authentication is enabled)
- **Request Body:**
  ```json
  {
    "url": "https://upstream.cdn.example/stream/master.m3u8",
    "headers": {
      "Origin": "https://upstream.example",
      "Referer": "https://upstream.example/embed/123"
    },
    "videoId": "550",
    "type": "movie"
  }
  ```
- **Response (`201 Created`):**
  ```json
  {
    "success": true,
    "session_id": "0123456789abcdef.eyJ...==.abcdef...",
    "playback_url": "https://worker-1.example.workers.dev/session/0123456789abcdef.../master.m3u8",
    "url": "https://worker-1.example.workers.dev/session/0123456789abcdef.../master.m3u8",
    "video_id": "550",
    "type": "movie",
    "expires_at": 1793297158
  }
  ```

### 2. Client Playback Stream

- **Master Playlist:** `GET /session/:sessionId/master.m3u8`
  - Returns `application/vnd.apple.mpegurl` with all variant and audio tracks rewritten to proxy URLs.
- **Proxied Resource:** `GET /session/:sessionId/res/:token/:filename`
  - Fetches variant playlists, `.ts` segments, audio tracks, or encryption keys using the session's server-side upstream headers.
  - Supports `Range: bytes=start-end` requests (returns `206 Partial Content`).
  - Standard CORS headers (`Access-Control-Allow-Origin: *`).

---

## Deployment & Configuration

### Environment Variables (`wrangler.toml` or Cloudflare Dashboard)

| Variable | Description | Default |
|---|---|---|
| `WORKER_SECRET` | Shared secret key required from Go backend in `X-Worker-Key` or `Authorization: Bearer <secret>` | `""` (open) |
| `SESSION_TTL_SECONDS` | Session lifespan in seconds | `14400` (4 hours) |
| `MAX_PLAYLIST_BYTES` | Maximum allowed playlist size in bytes | `5242880` (5MB) |
| `ALLOWED_UPSTREAM_DOMAINS` | Comma-separated domain whitelist (e.g. `hanerix.com,vidfast.vc`) | `""` (all public HTTPS) |

### Deploying 3 Independent Workers

To deploy 3 redundant workers for high availability:
```bash
# Worker 1
npx wrangler deploy --name hls-proxy-worker-1

# Worker 2
npx wrangler deploy --name hls-proxy-worker-2

# Worker 3
npx wrangler deploy --name hls-proxy-worker-3
```

Configure their URLs in the Go backend via `PROXY_WORKER_URLS`:
```bash
export PROXY_WORKER_URLS="https://hls-proxy-worker-1.workers.dev,https://hls-proxy-worker-2.workers.dev,https://hls-proxy-worker-3.workers.dev"
```

### Running Tests

```bash
npm test
```
