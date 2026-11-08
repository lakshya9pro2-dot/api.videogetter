/**
 * Session management for Netlify Serverless Proxy.
 * Provides unguessable session IDs, in-memory caching,
 * and stateless cryptographic fallback so sessions resolve across all serverless function instances.
 */

import { base64UrlEncode, base64UrlDecode } from "./security.mjs";

// In-memory LRU/TTL session cache for high-speed same-instance lookups
const inMemorySessions = new Map();
const MAX_IN_MEMORY_SESSIONS = 5000;

// Default session expiration: 4 hours
const DEFAULT_TTL_SECONDS = 14400;

/**
 * Returns or derives an HMAC CryptoKey for signing and validating session tokens.
 */
async function getSigningKey(env) {
  const secret = (env && (env.SESSION_SECRET || env.PROXY_SECRET || env.WORKER_SECRET)) || "vidara-netlify-proxy-default-salt-2026";
  const encoder = new TextEncoder();
  return crypto.subtle.importKey(
    "raw",
    encoder.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign", "verify"]
  );
}

/**
 * Creates an unguessable, signed session identifier containing tamper-proof state.
 *
 * @param {object} sessionData
 * @param {Record<string, string>} env
 * @returns {Promise<string>}
 */
export async function createSessionId(sessionData, env) {
  const key = await getSigningKey(env);

  // Generate 16 bytes of cryptographic randomness
  const randomBytes = new Uint8Array(16);
  crypto.getRandomValues(randomBytes);
  const randomHex = Array.from(randomBytes).map(b => b.toString(16).padStart(2, "0")).join("");

  const payload = JSON.stringify({
    rid: randomHex,
    u: sessionData.url,
    h: sessionData.headers || {},
    v: sessionData.videoId || "",
    t: sessionData.type || "",
    p: sessionData.provider || "",
    c: sessionData.createdAt,
    e: sessionData.expiresAt
  });

  const payloadEncoded = base64UrlEncode(payload);

  const signature = await crypto.subtle.sign(
    "HMAC",
    key,
    new TextEncoder().encode(payloadEncoded)
  );

  const sigBase64Url = Array.from(new Uint8Array(signature))
    .map(b => String.fromCharCode(b))
    .join("");
  const signatureEncoded = base64UrlEncode(sigBase64Url);

  return `${randomHex}.${payloadEncoded}.${signatureEncoded}`;
}

/**
 * Validates a signed session ID and extracts the decrypted payload if valid and not expired.
 *
 * @param {string} sessionId
 * @param {Record<string, string>} env
 * @returns {Promise<object|null>}
 */
export async function verifyAndDecodeSessionId(sessionId, env) {
  if (!sessionId || typeof sessionId !== "string") {
    return null;
  }

  const parts = sessionId.split(".");
  if (parts.length !== 3) {
    return null;
  }

  const [_, payloadEncoded, signatureEncoded] = parts;

  try {
    const key = await getSigningKey(env);

    const sigRaw = base64UrlDecode(signatureEncoded);
    const sigBytes = new Uint8Array(sigRaw.length);
    for (let i = 0; i < sigRaw.length; i++) {
      sigBytes[i] = sigRaw.charCodeAt(i);
    }

    const valid = await crypto.subtle.verify(
      "HMAC",
      key,
      sigBytes,
      new TextEncoder().encode(payloadEncoded)
    );

    if (!valid) {
      return null;
    }

    const payloadJson = base64UrlDecode(payloadEncoded);
    const data = JSON.parse(payloadJson);

    // Verify expiration timestamp
    if (data.e && Date.now() > data.e) {
      return null;
    }

    return {
      url: data.u,
      headers: data.h || {},
      videoId: data.v || "",
      type: data.t || "",
      provider: data.p || "",
      createdAt: data.c,
      expiresAt: data.e
    };
  } catch (err) {
    return null;
  }
}

/**
 * Creates and registers a new playback session.
 *
 * @param {object} params
 * @param {string} params.url Target upstream stream URL
 * @param {Record<string, string>} [params.headers] Custom upstream headers (Origin, Referer, User-Agent)
 * @param {string} [params.videoId] TMDB/Show identifier
 * @param {string} [params.type] 'movie' or 'tv'
 * @param {string} [params.provider] Provider name (e.g., 'streamhg', 'streamtape')
 * @param {Record<string, string>} env
 * @returns {Promise<{ id: string, expiresAt: number }>}
 */
export async function createSession(params, env) {
  const ttlSecs = parseInt((env && env.SESSION_TTL_SECONDS) || DEFAULT_TTL_SECONDS, 10);
  const now = Date.now();
  const expiresAt = now + ttlSecs * 1000;

  const sessionData = {
    url: params.url,
    headers: params.headers || {},
    videoId: params.videoId || "",
    type: params.type || "movie",
    provider: params.provider || "",
    createdAt: now,
    expiresAt
  };

  const sessionId = await createSessionId(sessionData, env);

  // Cache in-memory for instant lookup on same instance
  if (inMemorySessions.size >= MAX_IN_MEMORY_SESSIONS) {
    const oldestKey = inMemorySessions.keys().next().value;
    inMemorySessions.delete(oldestKey);
  }
  inMemorySessions.set(sessionId, sessionData);

  return {
    id: sessionId,
    expiresAt
  };
}

/**
 * Retrieves a session by its ID.
 * Falls back to cryptographic signature decoding across all distributed serverless instances.
 *
 * @param {string} sessionId
 * @param {Record<string, string>} env
 * @returns {Promise<object|null>}
 */
export async function getSession(sessionId, env) {
  if (!sessionId) {
    return null;
  }

  // 1. Check local in-memory cache
  const cached = inMemorySessions.get(sessionId);
  if (cached) {
    if (Date.now() > cached.expiresAt) {
      inMemorySessions.delete(sessionId);
      return null;
    }
    return cached;
  }

  // 2. Fall back to stateless cryptographic verification
  const decoded = await verifyAndDecodeSessionId(sessionId, env);
  if (decoded) {
    inMemorySessions.set(sessionId, decoded);
    return decoded;
  }

  return null;
}
