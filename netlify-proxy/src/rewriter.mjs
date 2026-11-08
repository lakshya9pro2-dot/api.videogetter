/**
 * HLS Playlist (.m3u8) parsing and URL rewriting utilities.
 * Handles Master playlists, Variant playlists, dynamic Audio tracks, Subtitles, and Encryption keys.
 * Preserves media bitstreams completely without transcoding.
 */

import { base64UrlEncode, base64UrlDecode } from "./security.mjs";

/**
 * Encodes an upstream URI into an unguessable resource token.
 *
 * @param {string} upstreamUrl
 * @returns {string}
 */
export function encodeResourceToken(upstreamUrl) {
  return base64UrlEncode(upstreamUrl);
}

/**
 * Decodes an upstream URI from a resource token.
 *
 * @param {string} token
 * @returns {string|null}
 */
export function decodeResourceToken(token) {
  try {
    return base64UrlDecode(token);
  } catch (_) {
    return null;
  }
}

/**
 * Resolves a relative or protocol-relative URI against a base URL.
 *
 * @param {string} relativeUri
 * @param {string} baseUrl
 * @returns {string}
 */
export function resolveUri(relativeUri, baseUrl) {
  try {
    return new URL(relativeUri, baseUrl).href;
  } catch (_) {
    return relativeUri;
  }
}

/**
 * Rewrites all media, audio, subtitle, and key URLs inside an HLS playlist (.m3u8)
 * so playback requests route securely through the Netlify proxy.
 *
 * @param {string} playlistContent Raw m3u8 text
 * @param {string} baseUrl Upstream URL where the playlist was fetched from
 * @param {string} sessionId Active playback session ID
 * @param {string} proxyBaseUrl Public origin of the Netlify proxy (e.g. https://my-proxy.netlify.app)
 * @returns {string} Rewritten playlist content
 */
export function rewritePlaylist(playlistContent, baseUrl, sessionId, proxyBaseUrl) {
  const cleanProxyBase = proxyBaseUrl.replace(/\/+$/, "");
  const sessionPrefix = `${cleanProxyBase}/session/${sessionId}`;
  const lines = playlistContent.split("\n");
  const rewritten = [];

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const trimmed = line.trim();

    // Preserve empty lines
    if (!trimmed) {
      rewritten.push(line);
      continue;
    }

    // 1. Rewrite Encryption Key tags: #EXT-X-KEY:METHOD=...,URI="..."
    if (trimmed.startsWith("#EXT-X-KEY:")) {
      rewritten.push(rewriteTagUriAttribute(line, "URI", baseUrl, sessionPrefix));
      continue;
    }

    // 2. Rewrite Audio & Subtitle Media tags: #EXT-X-MEDIA:TYPE=...,URI="..."
    if (trimmed.startsWith("#EXT-X-MEDIA:")) {
      rewritten.push(rewriteTagUriAttribute(line, "URI", baseUrl, sessionPrefix));
      continue;
    }

    // 3. Rewrite Session Data / Map tags: #EXT-X-MAP:URI="..."
    if (trimmed.startsWith("#EXT-X-MAP:")) {
      rewritten.push(rewriteTagUriAttribute(line, "URI", baseUrl, sessionPrefix));
      continue;
    }

    // 4. Pass through all other comments / directives unchanged
    if (trimmed.startsWith("#")) {
      rewritten.push(line);
      continue;
    }

    // 5. Line is a segment, variant sub-playlist, or audio URL
    const resolvedUrl = resolveUri(trimmed, baseUrl);
    const token = encodeResourceToken(resolvedUrl);
    const filename = extractFilename(resolvedUrl) || "segment.ts";
    const proxiedUri = `${sessionPrefix}/res/${token}/${filename}`;

    rewritten.push(proxiedUri);
  }

  return rewritten.join("\n");
}

/**
 * Helper to rewrite a URI="..." attribute inside an HLS tag.
 */
function rewriteTagUriAttribute(line, attrName, baseUrl, sessionPrefix) {
  const regex = new RegExp(`(${attrName}\\s*=\\s*["'])([^"']+)(["'])`, "gi");
  return line.replace(regex, (match, prefix, rawUri, suffix) => {
    const resolvedUrl = resolveUri(rawUri, baseUrl);
    const token = encodeResourceToken(resolvedUrl);
    const filename = extractFilename(resolvedUrl) || "resource.key";
    return `${prefix}${sessionPrefix}/res/${token}/${filename}${suffix}`;
  });
}

/**
 * Extracts the file basename from a URL path.
 */
function extractFilename(urlStr) {
  try {
    const parsed = new URL(urlStr);
    const pathname = parsed.pathname;
    const lastSlash = pathname.lastIndexOf("/");
    if (lastSlash !== -1) {
      const name = pathname.substring(lastSlash + 1);
      // Clean query and anchor remnants
      return name.split("?")[0].split("#")[0];
    }
  } catch (_) {}
  return "";
}
