/**
 * Universal Serverless HLS & MP4 Playback Proxy Handler.
 * Designed for Netlify Serverless Functions & Node.js runtimes.
 */

import {
  validateUpstreamUrl,
  verifyWorkerAuth,
  sanitizeUpstreamHeaders,
  CORS_HEADERS
} from "./security.mjs";
import { createSession, getSession, createSessionId } from "./session.mjs";
import {
  rewritePlaylist,
  decodeResourceToken,
  resolveUri
} from "./rewriter.mjs";
import {
  extractStreamHG,
  extractStreamtape
} from "./extractors.mjs";

const DEFAULT_MAX_PLAYLIST_BYTES = 5 * 1024 * 1024; // 5MB
const UPSTREAM_FETCH_TIMEOUT_MS = 25000;

export default {
  /**
   * Main fetch handler compatible with standard Web Request/Response API.
   *
   * @param {Request} request
   * @param {Record<string, string>} [env]
   * @param {object} [ctx]
   * @returns {Promise<Response>}
   */
  async fetch(request, env = process.env, ctx = {}) {
    const url = new URL(request.url);
    const method = request.method.toUpperCase();

    // 1. Handle CORS preflight
    if (method === "OPTIONS") {
      return new Response(null, {
        status: 204,
        headers: CORS_HEADERS
      });
    }

    const host = request.headers.get("x-forwarded-host") || request.headers.get("host") || url.host;
    const proto = request.headers.get("x-forwarded-proto") || url.protocol.replace(/:$/, "");
    const proxyBaseUrl = `${proto}://${host}`;

    // Normalize path by stripping .netlify/functions/proxy prefix if routed via Netlify redirects
    let pathname = url.pathname;
    const netlifyFuncPrefix = "/.netlify/functions/proxy";
    if (pathname.startsWith(netlifyFuncPrefix)) {
      pathname = pathname.slice(netlifyFuncPrefix.length) || "/";
    }

    console.log(`[req] ${method} ${pathname} range=${request.headers.get("range")} ip=${request.headers.get("x-forwarded-for")}`);

    // 2. Health check endpoint
    if (pathname === "/health" || pathname === "/api/health") {
      return jsonResponse({
        status: "ok",
        service: "netlify-serverless-playback-proxy",
        timestamp: Date.now()
      });
    }

    // 3. Create Session endpoint: POST /session or POST /api/session or POST /
    if (method === "POST" && (pathname === "/session" || pathname === "/api/session" || pathname === "/")) {
      return handleCreateSession(request, env, proxyBaseUrl);
    }

    // 3A. Extract & Proxy Session endpoint: POST /extract, POST /api/extract, POST /session/extract
    if (method === "POST" && (pathname === "/extract" || pathname === "/api/extract" || pathname === "/session/extract")) {
      return handleExtractAndCreateSession(request, env, proxyBaseUrl);
    }

    // 3B. Extract & Proxy GET endpoint: GET /extract/:provider/:id
    if (method === "GET" && pathname.startsWith("/extract/")) {
      const parts = pathname.slice("/extract/".length).split("/").filter(Boolean);
      if (parts.length >= 2) {
        return handleExtractByParams(parts[0], parts[1], request, env, proxyBaseUrl);
      }
    }

    // 4. Session Routes: /session/:sessionId/...
    if (pathname.startsWith("/session/")) {
      const rest = pathname.slice("/session/".length);
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
        return handleFetchAndRewritePlaylist(session.url, session, env, proxyBaseUrl);
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
        return handleProxyResource(session.url, session, env, proxyBaseUrl, request);
      }

      // 4C. Proxied Resource: /session/:sessionId/res/:token/:filename
      if (subPath.startsWith("/res/")) {
        const resParts = subPath.slice("/res/".length).split("/");
        const token = resParts[0];

        if (!token) {
          return jsonResponse({ error: "missing_resource_token" }, 400);
        }

        const upstreamUrl = decodeResourceToken(token);
        if (!upstreamUrl) {
          return jsonResponse({ error: "invalid_resource_token" }, 400);
        }

        return handleProxyResource(upstreamUrl, session, env, proxyBaseUrl, request);
      }

      // 4D. Relative fallback: /session/:sessionId/:filename (resolve against session.url)
      const relativeTarget = subPath.replace(/^\/+/, "");
      if (relativeTarget && !relativeTarget.includes("/")) {
        const resolved = resolveUri(relativeTarget, session.url);
        return handleProxyResource(resolved, session, env, proxyBaseUrl, request);
      }
    }

    // 5. Default 404 for unknown endpoints
    return jsonResponse({ error: "not_found", message: `Cannot ${method} ${pathname}` }, 404);
  }
};

/**
 * Handles POST request to create a new proxy playback session.
 */
async function handleCreateSession(request, env, proxyBaseUrl) {
  if (!verifyWorkerAuth(request, env)) {
    return jsonResponse({
      success: false,
      error: "unauthorized",
      message: "Valid X-Worker-Key, X-Proxy-Key, or Bearer token is required"
    }, 401);
  }

  let body;
  try {
    body = await request.json();
  } catch (_) {
    return jsonResponse({ success: false, error: "invalid_json_body" }, 400);
  }

  const { url: upstreamUrl, headers, videoId, type } = body || {};

  if (!upstreamUrl || typeof upstreamUrl !== "string") {
    return jsonResponse({ success: false, error: "missing_upstream_url" }, 400);
  }

  const allowInsecure = env && (env.ALLOW_INSECURE_TEST_UPSTREAM === "true" || env.NODE_ENV === "test");
  const validation = validateUpstreamUrl(upstreamUrl, env && env.ALLOWED_UPSTREAM_DOMAINS, allowInsecure);
  if (!validation.valid) {
    return jsonResponse({
      success: false,
      error: "disallowed_upstream_url",
      message: validation.error
    }, 403);
  }

  const sanitizedHeaders = sanitizeUpstreamHeaders(headers || {});
  const session = await createSession({
    url: upstreamUrl,
    headers: sanitizedHeaders,
    videoId: videoId ? String(videoId) : "",
    type: type ? String(type) : "movie"
  }, env);

  const cleanBase = proxyBaseUrl.replace(/\/+$/, "");
  const isDirectMp4 = upstreamUrl.includes(".mp4") || upstreamUrl.includes(".mkv") || upstreamUrl.includes("tapecontent.net");
  const playbackUrl = isDirectMp4
    ? `${cleanBase}/session/${session.id}/video.mp4`
    : `${cleanBase}/session/${session.id}/master.m3u8`;

  return jsonResponse({
    success: true,
    session_id: session.id,
    playback_url: playbackUrl,
    expires_at: session.expiresAt
  }, 201);
}

/**
 * Handles POST /extract to extract stream and create session in one call.
 */
async function handleExtractAndCreateSession(request, env, proxyBaseUrl) {
  if (!verifyWorkerAuth(request, env)) {
    return jsonResponse({
      success: false,
      error: "unauthorized",
      message: "Valid X-Worker-Key, X-Proxy-Key, or Bearer token is required"
    }, 401);
  }

  let body;
  try {
    body = await request.json();
  } catch (_) {
    return jsonResponse({ success: false, error: "invalid_json_body" }, 400);
  }

  const { provider, id, videoId, type } = body || {};
  if (!provider || !id) {
    return jsonResponse({
      success: false,
      error: "missing_parameters",
      message: "Both 'provider' ('streamhg', 'streamtape') and 'id' are required"
    }, 400);
  }

  return executeExtractionAndSession(provider, id, { videoId, type }, env, proxyBaseUrl);
}

/**
 * Handles GET /extract/:provider/:id
 */
async function handleExtractByParams(provider, id, request, env, proxyBaseUrl) {
  if (!verifyWorkerAuth(request, env)) {
    return jsonResponse({
      success: false,
      error: "unauthorized",
      message: "Valid X-Worker-Key, X-Proxy-Key, or Bearer token is required"
    }, 401);
  }

  const url = new URL(request.url);
  const videoId = url.searchParams.get("videoId") || id;
  const type = url.searchParams.get("type") || "movie";

  return executeExtractionAndSession(provider, id, { videoId, type }, env, proxyBaseUrl);
}

const extractionCache = new Map();
const inFlightExtractions = new Map();

async function getOrExtract(prov, id) {
  const cacheKey = `${prov}:${String(id).trim()}`;
  const now = Date.now();

  const cached = extractionCache.get(cacheKey);
  if (cached && cached.expiresAt > now) {
    return cached.data;
  }

  if (inFlightExtractions.has(cacheKey)) {
    return inFlightExtractions.get(cacheKey);
  }

  const promise = (async () => {
    try {
      let result;
      if (prov === "streamhg" || prov === "hanerix" || prov === "sh") {
        result = await extractStreamHG(id);
      } else if (prov === "streamtape" || prov === "st") {
        result = await extractStreamtape(id);
      } else {
        throw new Error(`Provider '${prov}' is not supported. Supported: 'streamhg', 'streamtape'`);
      }
      extractionCache.set(cacheKey, {
        data: result,
        expiresAt: Date.now() + 2 * 60 * 60 * 1000 // 2 hours
      });
      return result;
    } finally {
      inFlightExtractions.delete(cacheKey);
    }
  })();

  inFlightExtractions.set(cacheKey, promise);
  return promise;
}

/**
 * Internal worker logic for extracting from provider and creating a session.
 */
async function executeExtractionAndSession(provider, id, meta, env, proxyBaseUrl) {
  let extracted;
  const prov = provider.toLowerCase().trim();

  try {
    extracted = await getOrExtract(prov, id);
  } catch (err) {
    const isUnsupported = err.message.includes("not supported");
    return jsonResponse({
      success: false,
      error: isUnsupported ? "unsupported_provider" : "extraction_failed",
      message: err.message
    }, isUnsupported ? 400 : 502);
  }

  const allowInsecure = env && (env.ALLOW_INSECURE_TEST_UPSTREAM === "true" || env.NODE_ENV === "test");
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
    type: meta.type ? String(meta.type) : "movie",
    provider: prov
  }, env);

  const cleanBase = proxyBaseUrl.replace(/\/+$/, "");
  const playbackUrl = extracted.streamType === "mp4"
    ? `${cleanBase}/session/${session.id}/video.mp4`
    : `${cleanBase}/session/${session.id}/master.m3u8`;

  return jsonResponse({
    success: true,
    provider: prov,
    session_id: session.id,
    playback_url: playbackUrl,
    url: playbackUrl,
    direct_url: extracted.directUrl || extracted.streamUrl,
    tapecontent_url: extracted.tapecontentUrl || extracted.streamUrl,
    get_video_url: extracted.getVideoUrl || "",
    stream_type: extracted.streamType,
    title: extracted.title || "",
    video_id: id,
    type: meta.type || "movie",
    expires_at: session.expiresAt
  }, 200, {
    "Cache-Control": "public, max-age=3600, s-maxage=7200, stale-while-revalidate=600"
  });
}

/**
 * Fetches the master or variant playlist from upstream, rewrites its URLs, and returns to client.
 */
async function handleFetchAndRewritePlaylist(targetUrl, session, env, proxyBaseUrl) {
  const allowInsecure = env && (env.ALLOW_INSECURE_TEST_UPSTREAM === "true" || env.NODE_ENV === "test");
  const validation = validateUpstreamUrl(targetUrl, env && env.ALLOWED_UPSTREAM_DOMAINS, allowInsecure);
  if (!validation.valid) {
    return jsonResponse({ error: "disallowed_upstream_url", message: validation.error }, 403);
  }

  const reqHeaders = new Headers();
  for (const [k, v] of Object.entries(session.headers || {})) {
    reqHeaders.set(k, v);
  }
  if (!reqHeaders.has("User-Agent")) {
    reqHeaders.set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36");
  }

  const controller = new AbortController();
  const timeoutId = setTimeout(() => controller.abort(), UPSTREAM_FETCH_TIMEOUT_MS);

  let upstreamResp;
  try {
    upstreamResp = await fetch(targetUrl, {
      method: "GET",
      headers: reqHeaders,
      signal: controller.signal
    });
  } catch (err) {
    clearTimeout(timeoutId);
    return jsonResponse({
      error: "upstream_fetch_error",
      message: `Failed to fetch playlist from upstream: ${err.message}`
    }, 502);
  } finally {
    clearTimeout(timeoutId);
  }

  if (!upstreamResp.ok) {
    return jsonResponse({
      error: "upstream_playlist_error",
      status: upstreamResp.status,
      message: `Upstream playlist returned HTTP ${upstreamResp.status}`
    }, upstreamResp.status === 404 ? 404 : 502);
  }

  const maxBytes = parseInt((env && env.MAX_PLAYLIST_BYTES) || DEFAULT_MAX_PLAYLIST_BYTES, 10);
  const rawText = await upstreamResp.text();

  if (rawText.length > maxBytes) {
    return jsonResponse({
      error: "playlist_too_large",
      message: `Playlist exceeded max size of ${maxBytes} bytes`
    }, 502);
  }

  const sessionId = await createSessionId(session, env);
  const rewritten = rewritePlaylist(rawText, targetUrl, sessionId, proxyBaseUrl);

  const respHeaders = new Headers(CORS_HEADERS);
  respHeaders.set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8");
  respHeaders.set("Cache-Control", "no-cache, no-store, must-revalidate");

  return new Response(rewritten, {
    status: 200,
    headers: respHeaders
  });
}

/**
 * Proxies a media segment (.ts, .mp4, .m4s), key, or direct video stream with byte-range support.
 */
async function handleProxyResource(targetUrl, session, env, proxyBaseUrl, clientRequest) {
  const allowInsecure = env && (env.ALLOW_INSECURE_TEST_UPSTREAM === "true" || env.NODE_ENV === "test");
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
  let clientRange = clientRequest.headers.get("Range") || clientRequest.headers.get("range");
  const isDirectVideo = targetUrl.includes(".mp4") || targetUrl.includes(".mkv") || targetUrl.includes("tapecontent.net");
  if (isDirectVideo && clientRequest.method !== "HEAD") {
    // If open range or no range, clamp to 2MB to prevent 30-second timeout on multi-gigabyte video files
    if (!clientRange || clientRange.trim() === "bytes=0-") {
      clientRange = "bytes=0-2097151";
    } else if (clientRange.endsWith("-")) {
      const start = parseInt(clientRange.replace("bytes=", "").replace("-", ""), 10) || 0;
      clientRange = `bytes=${start}-${start + 2097151}`;
    }
  }
  if (clientRange) {
    reqHeaders.set("Range", clientRange);
  }

  console.log(`[proxy] target=${targetUrl} method=${clientRequest.method} range=${clientRange}`);

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
    let errorBody = await upstreamResp.text().catch(() => "");
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
  const isPlaylist =
    contentType.includes("mpegurl") ||
    contentType.includes("m3u8") ||
    targetUrl.includes(".m3u8");

  if (isPlaylist && upstreamResp.status === 200) {
    const rawText = await upstreamResp.text();
    const sessionId = await createSessionId(session, env);
    const rewritten = rewritePlaylist(rawText, targetUrl, sessionId, proxyBaseUrl);

    const respHeaders = new Headers(CORS_HEADERS);
    respHeaders.set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8");
    respHeaders.set("Cache-Control", "no-cache, no-store, must-revalidate");

    return new Response(rewritten, {
      status: 200,
      headers: respHeaders
    });
  }

  // Pass-through media stream/segment with range headers and open CORS
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
  if (targetUrl.includes(".mp4") || targetUrl.includes(".mkv") || targetUrl.includes("tapecontent.net") || targetUrl.includes(".ts") || targetUrl.includes(".m4s")) {
    respHeaders.set("Cache-Control", "public, max-age=86400, immutable");
  }

  return new Response(upstreamResp.body, {
    status: upstreamResp.status,
    headers: respHeaders
  });
}

/**
 * Helper to construct JSON response with CORS.
 */
function jsonResponse(data, status = 200, extraHeaders = {}) {
  const headers = new Headers(CORS_HEADERS);
  headers.set("Content-Type", "application/json; charset=utf-8");
  for (const [k, v] of Object.entries(extraHeaders)) {
    headers.set(k, v);
  }
  return new Response(JSON.stringify(data), {
    status,
    headers
  });
}
