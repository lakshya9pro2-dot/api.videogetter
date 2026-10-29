/**
 * HLS Playlist Rewriter for Cloudflare Worker HLS Proxy.
 * Parses and rewrites master and media playlists, rewriting all variant,
 * segment, audio track, subtitle, encryption key, and initialization segment URIs
 * while preserving all HLS tags, multi-audio groupings, and codecs intact.
 */

import { base64UrlEncode, base64UrlDecode } from "./security.js";

/**
 * Extracts a clean, safe filename from a URL path, defaulting to a given fallback.
 *
 * @param {string} urlStr
 * @param {string} defaultName
 * @returns {string}
 */
export function extractSafeFilename(urlStr, defaultName = "resource.bin") {
  try {
    const parsed = new URL(urlStr);
    const pathname = parsed.pathname;
    const lastSlash = pathname.lastIndexOf("/");
    if (lastSlash !== -1 && lastSlash < pathname.length - 1) {
      const name = pathname.substring(lastSlash + 1);
      // Clean non-alphanumeric chars except . - _
      const cleaned = name.replace(/[^a-zA-Z0-9.\-_]/g, "_");
      if (cleaned) {
        return cleaned;
      }
    }
  } catch (e) {
    // ignore
  }
  return defaultName;
}

/**
 * Encodes an upstream resource URL into a safe URL token.
 *
 * @param {string} upstreamUrl
 * @returns {string}
 */
export function encodeResourceToken(upstreamUrl) {
  return base64UrlEncode(upstreamUrl);
}

/**
 * Decodes an upstream resource URL from a URL token.
 *
 * @param {string} token
 * @returns {string|null}
 */
export function decodeResourceToken(token) {
  try {
    const decoded = base64UrlDecode(token);
    if (decoded && (decoded.startsWith("https://") || decoded.startsWith("http://"))) {
      return decoded;
    }
  } catch (e) {
    // ignore
  }
  return null;
}

/**
 * Resolves a potentially relative URI against a base URL.
 *
 * @param {string} rawUri
 * @param {string} basePlaylistUrl
 * @returns {string}
 */
export function resolveUri(rawUri, basePlaylistUrl) {
  const trimmed = rawUri.trim();
  try {
    return new URL(trimmed, basePlaylistUrl).href;
  } catch (e) {
    return trimmed;
  }
}

/**
 * Builds the rewritten proxy URL for an upstream resource.
 *
 * @param {string} workerBaseUrl e.g. "https://worker-1.example.workers.dev"
 * @param {string} sessionId
 * @param {string} resolvedUpstreamUrl
 * @param {string} fallbackFilename
 * @returns {string}
 */
export function buildProxyResourceUrl(workerBaseUrl, sessionId, resolvedUpstreamUrl, fallbackFilename) {
  const token = encodeResourceToken(resolvedUpstreamUrl);
  const filename = extractSafeFilename(resolvedUpstreamUrl, fallbackFilename);
  const cleanBase = workerBaseUrl.replace(/\/+$/, "");
  return `${cleanBase}/session/${sessionId}/res/${token}/${filename}`;
}

/**
 * Rewrites an HLS playlist (master or media variant).
 *
 * @param {string} m3u8Content Raw playlist text
 * @param {string} playlistUrl The URL where this playlist was fetched from
 * @param {string} sessionId Active playback session ID
 * @param {string} workerBaseUrl Worker origin (e.g. "https://worker.domain")
 * @returns {string} Rewritten playlist text
 */
export function rewritePlaylist(m3u8Content, playlistUrl, sessionId, workerBaseUrl) {
  const lines = m3u8Content.split(/\r?\n/);
  const rewrittenLines = [];

  // Regexes for tag attributes with URI="..."
  const reMediaUri = /(URI=")([^"]+)(")/gi;
  const reKeyUri = /(URI=")([^"]+)(")/gi;
  const reMapUri = /(URI=")([^"]+)(")/gi;

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const trimmed = line.trim();

    if (!trimmed) {
      rewrittenLines.push(line);
      continue;
    }

    // 1. Comments and Tags
    if (trimmed.startsWith("#")) {
      // Check #EXT-X-MEDIA: (Audio, Subtitles, Closed-Captions)
      if (trimmed.startsWith("#EXT-X-MEDIA:")) {
        const rewritten = line.replace(reMediaUri, (match, prefix, uri, suffix) => {
          const resolved = resolveUri(uri, playlistUrl);
          const proxied = buildProxyResourceUrl(workerBaseUrl, sessionId, resolved, "audio_or_sub.m3u8");
          return `${prefix}${proxied}${suffix}`;
        });
        rewrittenLines.push(rewritten);
        continue;
      }

      // Check #EXT-X-KEY: (Encryption keys)
      if (trimmed.startsWith("#EXT-X-KEY:")) {
        const rewritten = line.replace(reKeyUri, (match, prefix, uri, suffix) => {
          const resolved = resolveUri(uri, playlistUrl);
          const proxied = buildProxyResourceUrl(workerBaseUrl, sessionId, resolved, "key.bin");
          return `${prefix}${proxied}${suffix}`;
        });
        rewrittenLines.push(rewritten);
        continue;
      }

      // Check #EXT-X-MAP: (Initialization segments)
      if (trimmed.startsWith("#EXT-X-MAP:")) {
        const rewritten = line.replace(reMapUri, (match, prefix, uri, suffix) => {
          const resolved = resolveUri(uri, playlistUrl);
          const proxied = buildProxyResourceUrl(workerBaseUrl, sessionId, resolved, "init.mp4");
          return `${prefix}${proxied}${suffix}`;
        });
        rewrittenLines.push(rewritten);
        continue;
      }

      // All other tags preserved exactly as is
      rewrittenLines.push(line);
      continue;
    }

    // 2. URI lines (Segment URLs or Variant Stream URLs)
    const resolved = resolveUri(trimmed, playlistUrl);

    // Determine extension/fallback
    let fallback = "segment.ts";
    if (resolved.includes(".m3u8") || resolved.includes("/hls/")) {
      fallback = "variant.m3u8";
    }

    const proxied = buildProxyResourceUrl(workerBaseUrl, sessionId, resolved, fallback);
    rewrittenLines.push(proxied);
  }

  return rewrittenLines.join("\n");
}
