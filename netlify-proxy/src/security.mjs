/**
 * Security, authentication, and validation utilities for Netlify Serverless Proxy.
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
  /^\[?fd00:/i                              // IPv6 unique local
];

/**
 * Validates that an upstream URL is safe to fetch:
 * - Must be valid URL
 * - Must use HTTPS protocol strictly (or HTTP on localhost when in test)
 * - Must not target private/internal networks or cloud metadata
 * - Must match allowed domains if ALLOWED_UPSTREAM_DOMAINS is configured
 *
 * @param {string} urlStr
 * @param {string} [allowedDomainsConfig] Comma-separated domains or empty for any public HTTPS
 * @param {boolean} [allowInsecureTest]
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

  // 3. Domain allowlist check (if configured)
  const allowedDomains = (allowedDomainsConfig || "")
    .split(",")
    .map(d => d.trim().toLowerCase())
    .filter(Boolean);

  if (allowedDomains.length > 0) {
    const isDomainAllowed = allowedDomains.some(allowed => {
      return hostname === allowed || hostname.endsWith("." + allowed);
    });

    if (!isDomainAllowed) {
      return {
        valid: false,
        error: `Host '${hostname}' is not in the allowed upstream domains list`
      };
    }
  }

  return { valid: true, parsedUrl: parsed };
}

/**
 * Validates backend authorization key if WORKER_SECRET or PROXY_SECRET is set.
 *
 * @param {Request} request
 * @param {Record<string, string>} env
 * @returns {boolean}
 */
export function verifyWorkerAuth(request, env) {
  const secret = (env && (env.PROXY_SECRET || env.WORKER_SECRET)) || "";
  if (!secret) {
    return true; // No secret configured: open access
  }

  const authHeader = request.headers.get("Authorization") || "";
  const customHeader = request.headers.get("X-Worker-Key") || request.headers.get("X-Proxy-Key") || "";

  if (customHeader === secret) {
    return true;
  }

  if (authHeader.startsWith("Bearer ")) {
    return authHeader.slice("Bearer ".length).trim() === secret;
  }

  return false;
}

/**
 * Sanitizes headers forwarded from Go backend to upstream media server.
 * Prevents host header spoofing and dangerous proxy headers.
 *
 * @param {Record<string, string>} rawHeaders
 * @returns {Record<string, string>}
 */
export function sanitizeUpstreamHeaders(rawHeaders = {}) {
  const disallowed = new Set([
    "host",
    "cf-connecting-ip",
    "cf-ray",
    "x-forwarded-for",
    "x-forwarded-proto",
    "x-real-ip",
    "connection",
    "upgrade"
  ]);

  const sanitized = {};
  for (const [key, value] of Object.entries(rawHeaders)) {
    const lowerKey = key.toLowerCase();
    if (!disallowed.has(lowerKey) && typeof value === "string") {
      sanitized[key] = value;
    }
  }

  return sanitized;
}

/**
 * URL-safe Base64 encoding.
 *
 * @param {string} str
 * @returns {string}
 */
export function base64UrlEncode(str) {
  const b64 = Buffer.from(str, "utf8").toString("base64");
  return b64.replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/**
 * URL-safe Base64 decoding.
 *
 * @param {string} b64Url
 * @returns {string}
 */
export function base64UrlDecode(b64Url) {
  let b64 = b64Url.replace(/-/g, "+").replace(/_/g, "/");
  while (b64.length % 4) {
    b64 += "=";
  }
  return Buffer.from(b64, "base64").toString("utf8");
}

/**
 * Standard CORS headers for video streaming and HLS playback.
 */
export const CORS_HEADERS = {
  "Access-Control-Allow-Origin": "*",
  "Access-Control-Allow-Methods": "GET, HEAD, POST, OPTIONS",
  "Access-Control-Allow-Headers": "Range, Authorization, Content-Type, X-Worker-Key, X-Proxy-Key, X-Requested-With",
  "Access-Control-Expose-Headers": "Content-Range, Content-Length, Accept-Ranges, Content-Type",
  "Access-Control-Max-Age": "86400"
};
