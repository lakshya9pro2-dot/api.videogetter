/**
 * Session management for Cloudflare Worker HLS Proxy.
 * Provides unguessable session IDs, in-memory caching, optional KV integration,
 * and stateless cryptographic fallback so sessions resolve across all edge nodes.
 */

import { base64UrlEncode, base64UrlDecode } from "./security.js";

// In-memory LRU/TTL session cache for high-speed same-isolate lookups
const inMemorySessions = new Map();
const MAX_IN_MEMORY_SESSIONS = 5000;

// Default session expiration: 4 hours
const DEFAULT_TTL_SECONDS = 14400;

/**
 * Returns or derives an HMAC CryptoKey for signing and validating session tokens.
 */
async function getSigningKey(env) {
  const secret = (env && env.SESSION_SECRET) || (env && env.WORKER_SECRET) || "vidara-worker-default-secret-salt-2026";
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
    c: sessionData.createdAt,
    e: sessionData.expiresAt
  });

  const payloadEncoded = base64UrlEncode(payload);

  const signature = await crypto.subtle.sign(
    "HMAC",
    key,
    new TextEncoder().encode(payloadEncoded)
  );

  const sigEncoded = base64UrlEncode(
    String.fromCharCode(...new Uint8Array(signature))
  );

  // Unguessable composite session ID: <randomHex>_<payload>_<signature>
  return `${randomHex}.${payloadEncoded}.${sigEncoded}`;
}

/**
 * Parses and verifies an unguessable session ID.
 *
 * @param {string} sessionId
 * @param {Record<string, string>} env
 * @returns {Promise<object|null>}
 */
export async function verifySessionId(sessionId, env) {
  if (!sessionId || typeof sessionId !== "string") {
    return null;
  }

  const parts = sessionId.split(".");
  if (parts.length !== 3) {
    return null;
  }

  const [, payloadEncoded, sigEncoded] = parts;

  try {
    const key = await getSigningKey(env);
    const expectedSigRaw = base64UrlDecode(sigEncoded);
    const sigBytes = new Uint8Array(expectedSigRaw.length);
    for (let i = 0; i < expectedSigRaw.length; i++) {
      sigBytes[i] = expectedSigRaw.charCodeAt(i);
    }

    const isValid = await crypto.subtle.verify(
      "HMAC",
      key,
      sigBytes,
      new TextEncoder().encode(payloadEncoded)
    );

    if (!isValid) {
      return null;
    }

    const jsonStr = base64UrlDecode(payloadEncoded);
    const data = JSON.parse(jsonStr);

    return {
      id: sessionId,
      url: data.u,
      headers: data.h || {},
      videoId: data.v,
      type: data.t,
      createdAt: data.c,
      expiresAt: data.e
    };
  } catch (e) {
    return null;
  }
}

/**
 * Creates and registers a new playback session.
 *
 * @param {{ url: string, headers?: object, videoId?: string, type?: string }} params
 * @param {Record<string, string>} env
 * @returns {Promise<object>}
 */
export async function createSession(params, env) {
  const ttlSec = env && env.SESSION_TTL_SECONDS ? parseInt(env.SESSION_TTL_SECONDS, 10) : DEFAULT_TTL_SECONDS;
  const now = Date.now();
  const expiresAt = now + (ttlSec * 1000);

  const sessionObj = {
    url: params.url,
    headers: params.headers || {},
    videoId: params.videoId || "",
    type: params.type || "movie",
    createdAt: now,
    expiresAt: expiresAt
  };

  const sessionId = await createSessionId(sessionObj, env);
  sessionObj.id = sessionId;

  // 1. Cache in memory
  if (inMemorySessions.size >= MAX_IN_MEMORY_SESSIONS) {
    const firstKey = inMemorySessions.keys().next().value;
    inMemorySessions.delete(firstKey);
  }
  inMemorySessions.set(sessionId, sessionObj);

  // 2. Persist in KV if available
  if (env && env.SESSIONS_KV && typeof env.SESSIONS_KV.put === "function") {
    try {
      await env.SESSIONS_KV.put(sessionId, JSON.stringify(sessionObj), {
        expirationTtl: ttlSec
      });
    } catch (e) {
      // Non-fatal if KV fails, stateless token fallback remains active
    }
  }

  return sessionObj;
}

/**
 * Retrieves a session by ID.
 *
 * @param {string} sessionId
 * @param {Record<string, string>} env
 * @returns {Promise<object|null>}
 */
export async function getSession(sessionId, env) {
  if (!sessionId) {
    return null;
  }

  const now = Date.now();

  // 1. Fast in-memory lookup
  if (inMemorySessions.has(sessionId)) {
    const s = inMemorySessions.get(sessionId);
    if (s.expiresAt > now) {
      return s;
    }
    inMemorySessions.delete(sessionId);
    return null; // Expired
  }

  // 2. KV lookup if bound
  if (env && env.SESSIONS_KV && typeof env.SESSIONS_KV.get === "function") {
    try {
      const raw = await env.SESSIONS_KV.get(sessionId);
      if (raw) {
        const s = JSON.parse(raw);
        if (s.expiresAt > now) {
          inMemorySessions.set(sessionId, s);
          return s;
        }
      }
    } catch (e) {
      // Fall through to cryptographic verification
    }
  }

  // 3. Stateless cryptographic verification fallback
  const verified = await verifySessionId(sessionId, env);
  if (verified) {
    if (verified.expiresAt > now) {
      inMemorySessions.set(sessionId, verified);
      return verified;
    }
    return null; // Expired
  }

  return null;
}
