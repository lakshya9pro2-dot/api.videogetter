/**
 * Security and validation utilities for Cloudflare Worker HLS Proxy.
 */

// Disallowed private IPv4 ranges, loopback, and cloud metadata hosts
const PRIVATE_IP_REGEXES = [
  /^127\.\d{1,3}\.\d{1,3}\.\d{1,3}$/,       // Loopback 127.0.0.0/8
  /^10\.\d{1,3}\.\d{1,3}\.\d{1,3}$/,        // Private 10.0.0.0/8
  /^192\.168\.\d{1,3}\.\d{1,3}$/,           // Private 192.168.0.0/16
  /^172\.(1[6-9]|2\d|3[0-1])\.\d{1,3}\.\d{1,3}$/, // Private 172.16.0.0/12
  /^169\.254\.\d{1,3}\.\d{1,3}$/,          // Link-local / Cloud metadata 169.254.0.0/16
  /^0\.0\.0\.0$/,                           // Any local
  /^localhost$/i,
  /^\[?::1\]?$/,                            // IPv6 loopback
  /^\[?fe80:/i,                             // IPv6 link-local
  /^\[?fc00:/i,                             // IPv6 unique local
  /^\[?fd00:/i,                             // IPv6 unique local
];

/**
 * Validates that an upstream URL is safe to fetch:
 * - Must be valid URL
 * - Must use HTTPS protocol strictly
 * - Must not target private/internal networks or cloud metadata
 * - Must match allowed domains if ALLOWED_UPSTREAM_DOMAINS is configured
 *
 * @param {string} urlStr
 * @param {string} [allowedDomainsConfig] Comma-separated domains or empty for any public HTTPS
 * @returns {{ valid: boolean, error?: string, parsedUrl?: URL }}
 */
export function validateUpstreamUrl(urlStr, allowedDomainsConfig = "", allowInsecureTest = false) {
  if (!urlStr || typeof urlStr !== "string") {
    return { valid: false, error: "Missing or invalid upstream URL" };
  }

  let parsed;
  try {
    parsed = new URL(urlStr);
  } catch (e) {
    return { valid: false, error: "Malformed URL: " + e.message };
  }

  // Allow loopback HTTP only when explicitly permitted in automated local test suites
  if (allowInsecureTest && (parsed.hostname === "127.0.0.1" || parsed.hostname === "localhost")) {
    return { valid: true, parsedUrl: parsed };
  }

  // 1. Protocol check: strict HTTPS required
  if (parsed.protocol !== "https:") {
    return { valid: false, error: "Protocol must be HTTPS. Insecure HTTP is forbidden." };
  }

  const hostname = parsed.hostname.toLowerCase();

  // 2. SSRF check: block internal IPs and local addresses
  for (const regex of PRIVATE_IP_REGEXES) {
    if (regex.test(hostname)) {
      return { valid: false, error: "Access to private or local network hosts is strictly forbidden" };
    }
  }

  // Block decimal/hex IP representations like http://2130706433
  if (/^\d+$/.test(hostname) || /^0x[0-9a-f]+$/i.test(hostname)) {
    return { valid: false, error: "Numeric or hex IP hostnames are forbidden" };
  }

  // 3. Domain whitelist check (if configured)
  const allowedDomains = (allowedDomainsConfig || "")
    .split(",")
    .map(d => d.trim().toLowerCase())
    .filter(Boolean);

  if (allowedDomains.length > 0) {
    const isAllowed = allowedDomains.some(allowed => {
      return hostname === allowed || hostname.endsWith("." + allowed);
    });

    if (!isAllowed) {
      return {
        valid: false,
        error: `Hostname '${hostname}' is not in the configured upstream domain whitelist`
      };
    }
  }

  return { valid: true, parsedUrl: parsed };
}

/**
 * Validates backend-to-worker authentication if WORKER_SECRET is configured.
 *
 * @param {Request} request
 * @param {Record<string, string>} env
 * @returns {boolean}
 */
export function verifyWorkerAuth(request, env) {
  const secret = (env && env.WORKER_SECRET) ? env.WORKER_SECRET.trim() : "";
  if (!secret) {
    return true; // No secret configured, backend access is open
  }

  const headerKey = request.headers.get("X-Worker-Key") || request.headers.get("x-worker-key");
  if (headerKey && headerKey === secret) {
    return true;
  }

  const authHeader = request.headers.get("Authorization") || "";
  if (authHeader) {
    if (authHeader === secret) {
      return true;
    }
    if (authHeader.toLowerCase().startsWith("bearer ")) {
      const token = authHeader.slice(7).trim();
      if (token === secret) {
        return true;
      }
    }
  }

  return false;
}

/**
 * Sanitizes headers for upstream requests.
 * Only forwards safe headers and discards hop-by-hop headers.
 *
 * @param {Record<string, string>} headers
 * @returns {Record<string, string>}
 */
export function sanitizeUpstreamHeaders(headers = {}) {
  const clean = {};
  const allowed = [
    "origin",
    "referer",
    "user-agent",
    "accept",
    "accept-language",
    "range"
  ];

  for (const [key, val] of Object.entries(headers)) {
    if (typeof val === "string" && allowed.includes(key.toLowerCase())) {
      clean[key] = val;
    }
  }

  if (!clean["User-Agent"] && !clean["user-agent"]) {
    clean["User-Agent"] = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36";
  }

  return clean;
}

/**
 * URL-safe Base64 encoding.
 */
export function base64UrlEncode(str) {
  const bytes = new TextEncoder().encode(str);
  let binary = "";
  for (let i = 0; i < bytes.length; i++) {
    binary += String.fromCharCode(bytes[i]);
  }
  return btoa(binary)
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

/**
 * URL-safe Base64 decoding.
 */
export function base64UrlDecode(str) {
  let base64 = str.replace(/-/g, "+").replace(/_/g, "/");
  while (base64.length % 4) {
    base64 += "=";
  }
  const binary = atob(base64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return new TextDecoder().decode(bytes);
}

/**
 * Standard CORS headers to return to clients.
 */
export const CORS_HEADERS = {
  "Access-Control-Allow-Origin": "*",
  "Access-Control-Allow-Methods": "GET, HEAD, POST, OPTIONS",
  "Access-Control-Allow-Headers": "Content-Type, Range, Authorization, X-Requested-With",
  "Access-Control-Expose-Headers": "Content-Range, Content-Length, Accept-Ranges, Content-Type",
  "Access-Control-Max-Age": "86400"
};
