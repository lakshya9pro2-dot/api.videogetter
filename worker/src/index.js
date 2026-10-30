/**
 * Cloudflare Worker HLS Playback Proxy Entrypoint.
 *
 * Responsibilities:
 * - Validates backend session requests and upstream URLs (SSRF prevention).
 * - Manages short-lived unguessable playback sessions with server-side Origin/Referer.
 * - Rewrites master and variant HLS playlists, preserving multi-audio & subtitles.
 * - Proxies media segments (.ts, .m4s, .mp4, keys) with Range header and CORS support.
 * - Does NOT transcode media; preserves source media bitstreams completely.
 */

import {
  validateUpstreamUrl,
  verifyWorkerAuth,
  sanitizeUpstreamHeaders,
  CORS_HEADERS
} from "./security.js";
import { createSession, getSession } from "./session.js";
import {
  rewritePlaylist,
  decodeResourceToken,
  resolveUri
} from "./rewriter.js";
import {
  extractStreamHG,
  extractStreamtape
} from "./extractors.js";

const DEFAULT_MAX_PLAYLIST_BYTES = 5 * 1024 * 1024; // 5MB
const UPSTREAM_FETCH_TIMEOUT_MS = 15000;

export default {
  /**
   * Main fetch handler for Cloudflare Worker.
   *
   * @param {Request} request
   * @param {Record<string, string>} env
   * @param {object} ctx
   * @returns {Promise<Response>}
   */
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    const method = request.method.toUpperCase();

    // 1. Handle CORS preflight
    if (method === "OPTIONS") {
      return new Response(null, {
        status: 204,
        headers: CORS_HEADERS
      });
    }

    const workerBaseUrl = `${url.protocol}//${url.host}`;

    // 2. Health check endpoint
    if (url.pathname === "/health" || url.pathname === "/api/health") {
      return jsonResponse({
        status: "ok",
        service: "hls-playback-proxy-worker",
        timestamp: Date.now()
      });
    }

    // 3. Create Session endpoint: POST /session or POST / or POST /api/session
    if (method === "POST" && (url.pathname === "/session" || url.pathname === "/api/session" || url.pathname === "/")) {
      return handleCreateSession(request, env, workerBaseUrl);
    }

    // 3A. Extract & Proxy Session endpoint: POST /extract, POST /api/extract, POST /session/extract
    if (method === "POST" && (url.pathname === "/extract" || url.pathname === "/api/extract" || url.pathname === "/session/extract")) {
      return handleExtractAndCreateSession(request, env, workerBaseUrl);
    }

    // 3B. Extract & Proxy GET endpoint: GET /extract/:provider/:id (e.g. /extract/streamhg/ac7n6t4e5nma or /extract/streamtape/0A2vDYQz3wIbPQ6)
    if (method === "GET" && url.pathname.startsWith("/extract/")) {
      const parts = url.pathname.slice("/extract/".length).split("/").filter(Boolean);
      if (parts.length >= 2) {
        return handleExtractByParams(parts[0], parts[1], request, env, workerBaseUrl);
      }
    }

    // 4. Session Routes: /session/:sessionId/...
    if (url.pathname.startsWith("/session/")) {
      const rest = url.pathname.slice("/session/".length);
      const firstSlash = rest.indexOf("/");

      if (firstSlash === -1) {
        return jsonResponse({ error: "invalid_session_path" }, 400);
      }

      const sessionId = rest.substring(0, firstSlash);
      const subPath = rest.substring(firstSlash);

      // Verify session exists and is active
      const session = await getSession(sessionId, env);
      if (!session) {
        return jsonResponse({
          error: "session_expired_or_not_found",
          message: "The requested playback session does not exist or has expired"
        }, 410);
      }

      // 4A. Master Playlist: /session/:sessionId/master.m3u8 (or index.m3u8, etc.)
      if (subPath === "/master.m3u8" || subPath === "/index.m3u8" || subPath === "/playlist.m3u8") {
        return handleFetchAndRewritePlaylist(session.url, session, env, workerBaseUrl);
      }

      // 4B. Direct MP4 / Video stream: /session/:sessionId/video.mp4, /stream.mp4, /video, /stream, /play
      if (
        subPath === "/video.mp4" ||
        subPath === "/stream.mp4" ||
        subPath === "/video" ||
        subPath === "/stream" ||
        subPath === "/play" ||
        subPath.endsWith(".mp4") ||
        subPath.endsWith(".mkv")
      ) {
        return handleProxyResource(session.url, session, env, workerBaseUrl, request);
      }

      // 4B. Proxied Resource: /session/:sessionId/res/:token/:filename
      if (subPath.startsWith("/res/")) {
        const resParts = subPath.slice("/res/".length).split("/");
        const token = resParts[0];
        const filename = resParts.length > 1 ? resParts[1] : "";

        if (!token) {
          return jsonResponse({ error: "missing_resource_token" }, 400);
        }

        const upstreamUrl = decodeResourceToken(token);
        if (!upstreamUrl) {
          return jsonResponse({ error: "invalid_resource_token" }, 400);
        }

        return handleProxyResource(upstreamUrl, session, env, workerBaseUrl, request);
      }

      // 4C. Relative fallback: /session/:sessionId/:filename (resolve against session.url)
      const relativeTarget = subPath.replace(/^\/+/, "");
      if (relativeTarget && !relativeTarget.includes("/")) {
        const resolved = resolveUri(relativeTarget, session.url);
        return handleProxyResource(resolved, session, env, workerBaseUrl, request);
      }
    }

    // 5. Default 404 for unknown endpoints
    return jsonResponse({ error: "not_found", message: `Cannot ${method} ${url.pathname}` }, 404);
  }
};

/**
 * Handles POST request to create a new proxy playback session.
 */
async function handleCreateSession(request, env, workerBaseUrl) {
  // Check backend authorization
  if (!verifyWorkerAuth(request, env)) {
    return jsonResponse({
      success: false,
      error: "unauthorized",
      message: "Valid X-Worker-Key or Bearer token is required"
    }, 401);
  }

  let body;
  try {
    body = await request.json();
  } catch (e) {
    return jsonResponse({
      success: false,
      error: "invalid_json_body",
      message: "Request body must be valid JSON"
    }, 400);
  }

  if (!body || typeof body !== "object") {
    return jsonResponse({
      success: false,
      error: "invalid_request",
      message: "JSON object required"
    }, 400);
  }

  const upstreamUrl = (body.url || body.playback_url || "").trim();
  if (!upstreamUrl) {
    return jsonResponse({
      success: false,
      error: "missing_url",
      message: "'url' field is required"
    }, 400);
  }

  // Validate upstream URL (strict HTTPS + SSRF check + domain whitelist)
  const allowInsecure = env && env.ALLOW_INSECURE_TEST_UPSTREAM === "true";
  const validation = validateUpstreamUrl(upstreamUrl, env && env.ALLOWED_UPSTREAM_DOMAINS, allowInsecure);
  if (!validation.valid) {
    return jsonResponse({
      success: false,
      error: "disallowed_url",
      message: validation.error
    }, 403);
  }

  // Create session
  const session = await createSession({
    url: upstreamUrl,
    headers: sanitizeUpstreamHeaders(body.headers || {}),
    videoId: body.videoId ? String(body.videoId) : "",
    type: body.type ? String(body.type) : "movie"
  }, env);

  const cleanBase = workerBaseUrl.replace(/\/+$/, "");
  const playbackUrl = `${cleanBase}/session/${session.id}/master.m3u8`;

  return jsonResponse({
    success: true,
    session_id: session.id,
    playback_url: playbackUrl,
    url: playbackUrl,
    video_id: session.videoId,
    type: session.type,
    expires_at: Math.floor(session.expiresAt / 1000)
  }, 201);
}

/**
 * Handles POST request to extract stream from provider and register a proxy session.
 */
async function handleExtractAndCreateSession(request, env, workerBaseUrl) {
  if (!verifyWorkerAuth(request, env)) {
    return jsonResponse({
      success: false,
      error: "unauthorized",
      message: "Valid X-Worker-Key or Bearer token is required"
    }, 401);
  }

  let body;
  try {
    body = await request.json();
  } catch (e) {
    return jsonResponse({
      success: false,
      error: "invalid_json_body",
      message: "Request body must be valid JSON"
    }, 400);
  }

  if (!body || typeof body !== "object") {
    return jsonResponse({
      success: false,
      error: "invalid_request",
      message: "JSON object required"
    }, 400);
  }

  const provider = (body.provider || body.source || "").toLowerCase().trim();
  const id = (body.id || body.file_code || body.filecode || body.url || "").trim();

  if (!provider || !id) {
    return jsonResponse({
      success: false,
      error: "missing_parameters",
      message: "'provider' and 'id' fields are required"
    }, 400);
  }

  return executeExtractionAndSession(provider, id, body, env, workerBaseUrl);
}

/**
 * Handles GET request to extract stream by URL parameters.
 */
async function handleExtractByParams(provider, id, request, env, workerBaseUrl) {
  if (!verifyWorkerAuth(request, env)) {
    return jsonResponse({
      success: false,
      error: "unauthorized",
      message: "Valid X-Worker-Key or Bearer token is required"
    }, 401);
  }
  return executeExtractionAndSession(provider, id, {}, env, workerBaseUrl);
}

/**
 * Internal executor for provider extraction and session creation.
 */
async function executeExtractionAndSession(provider, id, meta, env, workerBaseUrl) {
  let extracted;
  const prov = provider.toLowerCase().trim();

  try {
    if (prov === "streamhg" || prov === "hanerix" || prov === "sh") {
      extracted = await extractStreamHG(id);
    } else if (prov === "streamtape" || prov === "st") {
      extracted = await extractStreamtape(id);
    } else {
      return jsonResponse({
        success: false,
        error: "unsupported_provider",
        message: `Provider '${provider}' is not supported. Supported: 'streamhg', 'streamtape'`
      }, 400);
    }
  } catch (err) {
    return jsonResponse({
      success: false,
      error: "extraction_failed",
      message: err.message
    }, 502);
  }

  const allowInsecure = env && env.ALLOW_INSECURE_TEST_UPSTREAM === "true";
  const validation = validateUpstreamUrl(extracted.streamUrl, env && env.ALLOWED_UPSTREAM_DOMAINS, allowInsecure);
  if (!validation.valid) {
    return jsonResponse({
      success: false,
      error: "disallowed_extracted_url",
      message: validation.error
    }, 403);
  }

  const session = await createSession({
    url: extracted.streamUrl,
    headers: sanitizeUpstreamHeaders(extracted.headers || {}),
    videoId: meta.videoId ? String(meta.videoId) : id,
    type: meta.type ? String(meta.type) : "movie"
  }, env);

  const cleanBase = workerBaseUrl.replace(/\/+$/, "");
  const playbackUrl = extracted.streamType === "mp4"
    ? `${cleanBase}/session/${session.id}/video.mp4`
    : `${cleanBase}/session/${session.id}/master.m3u8`;

  return jsonResponse({
    success: true,
    provider: prov,
    session_id: session.id,
    playback_url: playbackUrl,
    url: playbackUrl,
    direct_url: extracted.streamUrl,
    stream_type: extracted.streamType,
    title: extracted.title,
    video_id: session.videoId,
    type: session.type,
    expires_at: Math.floor(session.expiresAt / 1000)
  }, 201);
}

/**
 * Fetches an HLS playlist from upstream, rewrites its URI references,
 * and returns it with proper MIME type and CORS headers.
 */
async function handleFetchAndRewritePlaylist(playlistUrl, session, env, workerBaseUrl) {
  const allowInsecure = env && env.ALLOW_INSECURE_TEST_UPSTREAM === "true";
  const validation = validateUpstreamUrl(playlistUrl, env && env.ALLOWED_UPSTREAM_DOMAINS, allowInsecure);
  if (!validation.valid) {
    return jsonResponse({ error: "disallowed_playlist_url", message: validation.error }, 403);
  }

  const reqHeaders = new Headers();
  for (const [k, v] of Object.entries(session.headers || {})) {
    reqHeaders.set(k, v);
  }
  if (!reqHeaders.has("User-Agent")) {
    reqHeaders.set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36");
  }
  reqHeaders.set("Accept", "*/*");

  const controller = new AbortController();
  const timeoutId = setTimeout(() => controller.abort(), UPSTREAM_FETCH_TIMEOUT_MS);

  let upstreamResp;
  try {
    upstreamResp = await fetch(playlistUrl, {
      method: "GET",
      headers: reqHeaders,
      signal: controller.signal
    });
  } catch (err) {
    clearTimeout(timeoutId);
    return jsonResponse({
      error: "upstream_fetch_error",
      message: `Failed to connect to upstream: ${err.message}`
    }, 502);
  } finally {
    clearTimeout(timeoutId);
  }

  if (!upstreamResp.ok) {
    const errorBody = await upstreamResp.text().catch(() => "");
    return jsonResponse({
      error: "upstream_error",
      status: upstreamResp.status,
      message: `Upstream playlist returned HTTP ${upstreamResp.status}`,
      upstream_url: playlistUrl,
      upstream_detail: errorBody.slice(0, 300).trim()
    }, upstreamResp.status === 404 ? 404 : 502);
  }

  const maxBytes = env && env.MAX_PLAYLIST_BYTES ? parseInt(env.MAX_PLAYLIST_BYTES, 10) : DEFAULT_MAX_PLAYLIST_BYTES;
  const rawText = await upstreamResp.text();

  if (rawText.length > maxBytes) {
    return jsonResponse({
      error: "playlist_too_large",
      message: `Playlist size exceeds maximum permitted limit (${maxBytes} bytes)`
    }, 413);
  }

  // Rewrite all URIs in playlist
  const rewritten = rewritePlaylist(rawText, playlistUrl, session.id, workerBaseUrl);

  const respHeaders = new Headers(CORS_HEADERS);
  respHeaders.set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8");
  respHeaders.set("Cache-Control", "no-cache, no-store, must-revalidate");

  return new Response(rewritten, {
    status: 200,
    headers: respHeaders
  });
}

/**
 * Proxies a media segment (.ts, .mp4, .m4s), key, or nested sub-playlist.
 */
async function handleProxyResource(targetUrl, session, env, workerBaseUrl, clientRequest) {
  const allowInsecure = env && env.ALLOW_INSECURE_TEST_UPSTREAM === "true";
  const validation = validateUpstreamUrl(targetUrl, env && env.ALLOWED_UPSTREAM_DOMAINS, allowInsecure);
  if (!validation.valid) {
    return jsonResponse({ error: "disallowed_resource_url", message: validation.error }, 403);
  }

  const reqHeaders = new Headers();
  for (const [k, v] of Object.entries(session.headers || {})) {
    reqHeaders.set(k, v);
  }
  if (!reqHeaders.has("User-Agent")) {
    reqHeaders.set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36");
  }

  // Forward client Range header if requested (vital for video seeking and partial segments)
  const clientRange = clientRequest.headers.get("Range") || clientRequest.headers.get("range");
  if (clientRange) {
    reqHeaders.set("Range", clientRange);
  }

  const controller = new AbortController();
  const timeoutId = setTimeout(() => controller.abort(), UPSTREAM_FETCH_TIMEOUT_MS);

  let upstreamResp;
  try {
    upstreamResp = await fetch(targetUrl, {
      method: clientRequest.method === "HEAD" ? "HEAD" : "GET",
      headers: reqHeaders,
      signal: controller.signal
    });
  } catch (err) {
    clearTimeout(timeoutId);
    return jsonResponse({
      error: "upstream_resource_error",
      message: `Failed to fetch resource from upstream: ${err.message}`
    }, 502);
  } finally {
    clearTimeout(timeoutId);
  }

  if (!upstreamResp.ok && upstreamResp.status !== 206) {
    const errorBody = await upstreamResp.text().catch(() => "");
    return jsonResponse({
      error: "upstream_error",
      status: upstreamResp.status,
      message: `Upstream resource returned HTTP ${upstreamResp.status}`,
      upstream_url: targetUrl,
      upstream_detail: errorBody.slice(0, 300).trim()
    }, upstreamResp.status === 404 ? 404 : 502);
  }

  // Check if the returned resource is actually another playlist (nested variant or audio track)
  const contentType = (upstreamResp.headers.get("Content-Type") || "").toLowerCase();
  const isPlaylist = targetUrl.includes(".m3u8") ||
    contentType.includes("application/vnd.apple.mpegurl") ||
    contentType.includes("application/x-mpegurl");

  if (isPlaylist && clientRequest.method !== "HEAD") {
    const rawText = await upstreamResp.text();
    const rewritten = rewritePlaylist(rawText, targetUrl, session.id, workerBaseUrl);

    const respHeaders = new Headers(CORS_HEADERS);
    respHeaders.set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8");
    respHeaders.set("Cache-Control", "no-cache, no-store, must-revalidate");

    return new Response(rewritten, {
      status: upstreamResp.status,
      headers: respHeaders
    });
  }

  // Media segment or binary resource (video/mp2t, video/mp4, audio/aac, etc.)
  const respHeaders = new Headers(CORS_HEADERS);
  for (const h of ["Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"]) {
    const val = upstreamResp.headers.get(h);
    if (val) {
      respHeaders.set(h, val);
    }
  }

  // Ensure default video/mp2t if missing on .ts files
  if (targetUrl.includes(".ts") && !respHeaders.has("Content-Type")) {
    respHeaders.set("Content-Type", "video/mp2t");
  }

  respHeaders.set("Accept-Ranges", "bytes");

  return new Response(upstreamResp.body, {
    status: upstreamResp.status,
    headers: respHeaders
  });
}

/**
 * Helper to construct JSON response with CORS.
 */
function jsonResponse(data, status = 200) {
  const headers = new Headers(CORS_HEADERS);
  headers.set("Content-Type", "application/json; charset=utf-8");
  return new Response(JSON.stringify(data), {
    status,
    headers
  });
}
