# Netlify Serverless HLS & MP4 Streaming Proxy

A dedicated, scalable serverless playback proxy built for **Netlify Functions V2**.

It features:
- In-function **Streamtape** and **StreamHG** stream extraction without IP/ASN mismatch errors.
- Dynamic **HLS playlist rewriting** (`.m3u8`), preserving all multi-audio tracks and subtitles.
- Direct **MP4 video streaming** with full HTTP `Range` support and **`HTTP 206 Partial Content`**.
- Stateless, cryptographic **session management** (HMAC-SHA256) resolving across all distributed serverless edge nodes.
- Strict **SSRF protection** blocking access to internal IPs, metadata endpoints, and loopback.

---

## Directory Structure

```
netlify-proxy/
├── netlify.toml                        # Netlify configuration & function routing
├── package.json                        # Node dependencies & scripts
├── netlify/
│   └── functions/
│       └── proxy.mjs                   # Netlify Functions V2 serverless entrypoint
├── src/
│   ├── index.mjs                       # Core request handler (sessions, extraction, proxying)
│   ├── extractors.mjs                  # Streamtape & StreamHG extractors
│   ├── rewriter.mjs                    # HLS playlist rewriter & resource token coder
│   ├── security.mjs                    # SSRF protection, CORS headers, auth check
│   └── session.mjs                     # HMAC signed session manager
├── server.mjs                          # Standalone local test server (http://localhost:8888)
└── test/
    └── proxy.test.mjs                  # Test suite verifying Range requests & extraction
```

---

## 1. Local Testing

You can run the serverless proxy locally without needing the Netlify CLI:

```bash
cd netlify-proxy
npm test          # Run all automated unit and integration tests
npm start         # Starts standalone test server on http://localhost:8888
```

### Testing Endpoints Locally

* **Health Check**:
  ```bash
  curl -s http://localhost:8888/health
  ```

* **Extract Streamtape (e.g. Nilakanta 1107009 / `0A2vDYQz3wIbPQ6`)**:
  ```bash
  curl -s http://localhost:8888/extract/st/0A2vDYQz3wIbPQ6
  ```

* **Test HTTP 206 Partial Content Range Streaming**:
  ```bash
  curl -i -s -H "Range: bytes=0-100" "<playback_url_from_previous_command>"
  ```
  Returns `HTTP/1.1 206 Partial Content` with `Content-Range: bytes 0-100/...`!

---

## 2. Deploying to Netlify

### Option A: Using Netlify CLI (Recommended)

1. Install Netlify CLI (if not already installed):
   ```bash
   npm install -g netlify-cli
   ```

2. From within the `netlify-proxy` directory, log in and deploy:
   ```bash
   cd netlify-proxy
   netlify login
   netlify init
   netlify deploy --prod
   ```

### Option B: Deploying via Netlify Git Integration

1. Push your repository to GitHub / GitLab.
2. In the Netlify Dashboard, click **Add new site** &rarr; **Import an existing project**.
3. Set **Base directory** to:
   ```
   netlify-proxy
   ```
4. Build command: *(leave empty)*
5. Publish directory: *(leave empty)*
6. Click **Deploy**.

---

## 3. Environment Variables (Netlify Site Settings)

In the Netlify Dashboard (**Site configuration** &rarr; **Environment variables**):

| Variable | Description | Default |
| :--- | :--- | :--- |
| `PROXY_SECRET` | Secret key required in `Authorization: Bearer <key>` or `X-Proxy-Key` | *(Empty = Open access)* |
| `SESSION_TTL_SECONDS` | Session duration in seconds | `14400` (4 hours) |
| `MAX_PLAYLIST_BYTES` | Maximum allowed playlist file size in bytes | `5242880` (5MB) |
| `ALLOWED_UPSTREAM_DOMAINS` | Comma-separated list of allowed domains | *(Empty = all public HTTPS)* |

---

## 4. Connecting to the Go Backend

Once deployed to Netlify (e.g. `https://my-stream-proxy.netlify.app`), connect it to your Go backend:

### In `.env`:
```env
CF_WORKERS_URL=https://my-stream-proxy.netlify.app
```

### In `config.go`:
```go
// Replace worker URL with your Netlify Serverless Proxy URL:
workerURLs := []string{"https://my-stream-proxy.netlify.app"}
```

The Go backend communicates with Netlify Serverless Proxy using the exact same API contract as the Cloudflare Worker, with **full Streamtape ASN compatibility and zero 403 blocks**.
