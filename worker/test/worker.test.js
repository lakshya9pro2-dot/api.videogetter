/**
 * Comprehensive test suite for Cloudflare Worker HLS Proxy.
 * Uses native Node.js test runner (node:test) and built-in HTTP server for upstream simulation.
 */

import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import worker from "../src/index.js";
import { validateUpstreamUrl, verifyWorkerAuth, base64UrlEncode } from "../src/security.js";
import { rewritePlaylist } from "../src/rewriter.js";

let mockServer;
let mockPort;
let mockBaseUrl;

before(async () => {
  // Setup mock upstream server
  mockServer = http.createServer((req, res) => {
    const url = new URL(req.url, `http://127.0.0.1:${mockPort}`);

    // CORS
    res.setHeader("Access-Control-Allow-Origin", "*");

    // Check upstream headers
    if (url.pathname === "/check-headers") {
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify({
        referer: req.headers["referer"] || "",
        origin: req.headers["origin"] || "",
        userAgent: req.headers["user-agent"] || ""
      }));
      return;
    }

    // Master playlist with multiple variants, multi-language audio, and subtitles
    if (url.pathname === "/master.m3u8") {
      const masterContent = `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="English",DEFAULT=YES,AUTOSELECT=YES,LANGUAGE="en",URI="audio/en.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="Hindi",DEFAULT=NO,AUTOSELECT=YES,LANGUAGE="hi",URI="https://cdn.mock.test/audio/hi.m3u8"
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="English",DEFAULT=YES,LANGUAGE="en",URI="subs/en.vtt"
#EXT-X-STREAM-INF:BANDWIDTH=4500000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2",AUDIO="audio",SUBTITLES="subs"
variants/1080p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2200000,RESOLUTION=1280x720,CODECS="avc1.4d401f,mp4a.40.2",AUDIO="audio",SUBTITLES="subs"
https://cdn.mock.test/variants/720p.m3u8
`;
      res.writeHead(200, { "Content-Type": "application/vnd.apple.mpegurl" });
      res.end(masterContent);
      return;
    }

    // Variant media playlist with segments, init map, and key
    if (url.pathname === "/variants/1080p.m3u8") {
      const variantContent = `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-KEY:METHOD=AES-128,URI="keys/key1.bin"
#EXT-X-MAP:URI="init.mp4"
#EXTINF:6.000,
seg_001.ts
#EXTINF:6.000,
https://cdn.mock.test/segments/seg_002.ts
#EXT-X-ENDLIST
`;
      res.writeHead(200, { "Content-Type": "application/vnd.apple.mpegurl" });
      res.end(variantContent);
      return;
    }

    // TS segment with byte-range support
    if (url.pathname.endsWith(".ts")) {
      const dummyData = Buffer.alloc(1024, 0x47); // 1KB of sync bytes
      const range = req.headers["range"];
      if (range) {
        // e.g. bytes=0-99
        const m = range.match(/bytes=(\d+)-(\d+)?/);
        if (m) {
          const start = parseInt(m[1], 10);
          const end = m[2] ? parseInt(m[2], 10) : dummyData.length - 1;
          const chunk = dummyData.subarray(start, end + 1);
          res.writeHead(206, {
            "Content-Type": "video/mp2t",
            "Content-Range": `bytes ${start}-${end}/${dummyData.length}`,
            "Content-Length": String(chunk.length),
            "Accept-Ranges": "bytes"
          });
          res.end(chunk);
          return;
        }
      }
      res.writeHead(200, {
        "Content-Type": "video/mp2t",
        "Content-Length": String(dummyData.length),
        "Accept-Ranges": "bytes"
      });
      res.end(dummyData);
      return;
    }

    // 404 for other endpoints
    res.writeHead(404, { "Content-Type": "text/plain" });
    res.end("Not found");
  });

  await new Promise(resolve => {
    mockServer.listen(0, "127.0.0.1", () => {
      mockPort = mockServer.address().port;
      mockBaseUrl = `http://127.0.0.1:${mockPort}`;
      resolve();
    });
  });
});

after(() => {
  if (mockServer) {
    mockServer.close();
  }
});

// ============================================================================
// PART C.1: SECURITY & SSRF TESTS
// ============================================================================

test("Security: Rejects HTTP protocol (requires HTTPS)", () => {
  const res = validateUpstreamUrl("http://example.com/video.m3u8");
  assert.strictEqual(res.valid, false);
  assert.match(res.error, /HTTPS/);
});

test("Security: Rejects localhost and loopback IP (SSRF protection)", () => {
  assert.strictEqual(validateUpstreamUrl("https://localhost/master.m3u8").valid, false);
  assert.strictEqual(validateUpstreamUrl("https://127.0.0.1/master.m3u8").valid, false);
  assert.strictEqual(validateUpstreamUrl("https://127.0.1.1/master.m3u8").valid, false);
});

test("Security: Rejects private LAN IP ranges (SSRF protection)", () => {
  assert.strictEqual(validateUpstreamUrl("https://10.0.0.1/master.m3u8").valid, false);
  assert.strictEqual(validateUpstreamUrl("https://192.168.1.100/master.m3u8").valid, false);
  assert.strictEqual(validateUpstreamUrl("https://172.16.0.5/master.m3u8").valid, false);
});

test("Security: Rejects cloud metadata service (169.254.169.254)", () => {
  assert.strictEqual(validateUpstreamUrl("https://169.254.169.254/latest/meta-data").valid, false);
});

test("Security: Permits valid public HTTPS URLs", () => {
  assert.strictEqual(validateUpstreamUrl("https://hanerix.com/stream/master.m3u8").valid, true);
  assert.strictEqual(validateUpstreamUrl("https://cdn.example.org/stream/1080p.m3u8").valid, true);
});

test("Security: Enforces ALLOWED_UPSTREAM_DOMAINS whitelist when configured", () => {
  const allowed = "hanerix.com,streamtape.com,vidfast.vc";
  assert.strictEqual(validateUpstreamUrl("https://hanerix.com/e/abc", allowed).valid, true);
  assert.strictEqual(validateUpstreamUrl("https://sub.streamtape.com/v/123", allowed).valid, true);
  assert.strictEqual(validateUpstreamUrl("https://unauthorized.evil.com/video.m3u8", allowed).valid, false);
});

test("Security: Validates Worker authentication secret", () => {
  const env = { WORKER_SECRET: "super-secret-key-123" };

  const validReq = new Request("https://worker.test/session", {
    method: "POST",
    headers: { "X-Worker-Key": "super-secret-key-123" }
  });
  assert.strictEqual(verifyWorkerAuth(validReq, env), true);

  const bearerReq = new Request("https://worker.test/session", {
    method: "POST",
    headers: { "Authorization": "Bearer super-secret-key-123" }
  });
  assert.strictEqual(verifyWorkerAuth(bearerReq, env), true);

  const invalidReq = new Request("https://worker.test/session", {
    method: "POST",
    headers: { "X-Worker-Key": "wrong-key" }
  });
  assert.strictEqual(verifyWorkerAuth(invalidReq, env), false);
});

// ============================================================================
// PART C.2: WORKER ENDPOINT & SESSION CREATION TESTS
// ============================================================================

test("Worker POST: Successfully creates session with unguessable URL", async () => {
  const env = { WORKER_SECRET: "test-secret" };
  const req = new Request("https://worker-1.example.workers.dev/session", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-Worker-Key": "test-secret"
    },
    body: JSON.stringify({
      url: "https://hanerix.com/stream/xyz/master.m3u8",
      headers: {
        "Origin": "https://hanerix.com",
        "Referer": "https://hanerix.com/e/xyz"
      },
      videoId: "550",
      type: "movie"
    })
  });

  const res = await worker.fetch(req, env);
  assert.strictEqual(res.status, 201);

  const json = await res.json();
  assert.strictEqual(json.success, true);
  assert.ok(json.session_id);
  assert.match(json.playback_url, /^https:\/\/worker-1\.example\.workers\.dev\/session\/.+\/master\.m3u8$/);
  assert.ok(json.expires_at > Date.now() / 1000);
});

test("Worker POST: Rejects request with missing url", async () => {
  const req = new Request("https://worker.test/session", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ videoId: "550" })
  });

  const res = await worker.fetch(req, {});
  assert.strictEqual(res.status, 400);
  const json = await res.json();
  assert.strictEqual(json.success, false);
});

test("Worker POST: Rejects SSRF target in session request", async () => {
  const req = new Request("https://worker.test/session", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      url: "https://127.0.0.1/admin/master.m3u8"
    })
  });

  const res = await worker.fetch(req, {});
  assert.strictEqual(res.status, 403);
});

// ============================================================================
// PART C.3: PLAYLIST REWRITING & PRESERVATION TESTS
// ============================================================================

test("Rewriter: Rewrites variant streams and relative URLs correctly", () => {
  const playlist = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080
1080p.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2500000,RESOLUTION=1280x720
https://cdn.example.com/hls/720p.m3u8
`;
  const base = "https://source.example.com/master.m3u8";
  const rewritten = rewritePlaylist(playlist, base, "test-sess", "https://worker.dev");

  // Relative 1080p.m3u8 resolved to https://source.example.com/1080p.m3u8 and proxied
  assert.match(rewritten, /https:\/\/worker\.dev\/session\/test-sess\/res\//);
  assert.match(rewritten, /1080p\.m3u8/);
  // Absolute 720p.m3u8 preserved and proxied
  assert.match(rewritten, /720p\.m3u8/);
});

test("Rewriter: Preserves multi-language audio tracks and subtitles", () => {
  const playlist = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="English",DEFAULT=YES,LANGUAGE="en",URI="audio_en.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="हिन्दी",DEFAULT=NO,LANGUAGE="hi",URI="audio_hi.m3u8"
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="sub",NAME="English",DEFAULT=YES,LANGUAGE="en",URI="sub_en.vtt"
`;
  const base = "https://source.example.com/master.m3u8";
  const rewritten = rewritePlaylist(playlist, base, "test-sess", "https://worker.dev");

  // All tags and attributes preserved
  assert.match(rewritten, /GROUP-ID="audio"/);
  assert.match(rewritten, /NAME="English"/);
  assert.match(rewritten, /NAME="हिन्दी"/);
  assert.match(rewritten, /LANGUAGE="hi"/);
  assert.match(rewritten, /TYPE=SUBTITLES/);
  // URIs proxied through worker session
  assert.match(rewritten, /URI="https:\/\/worker\.dev\/session\/test-sess\/res\//);
});

test("Rewriter: Rewrites media segments (.ts), keys, and init maps", () => {
  const playlist = `#EXTM3U
#EXT-X-KEY:METHOD=AES-128,URI="enc.key"
#EXT-X-MAP:URI="init.mp4"
#EXTINF:6.0,
seg_001.ts
#EXTINF:6.0,
seg_002.ts
`;
  const base = "https://source.example.com/hls/1080p.m3u8";
  const rewritten = rewritePlaylist(playlist, base, "sess-1", "https://worker.dev");

  assert.match(rewritten, /#EXT-X-KEY:METHOD=AES-128,URI="https:\/\/worker\.dev\/session\/sess-1\/res\/.+\/enc\.key"/);
  assert.match(rewritten, /#EXT-X-MAP:URI="https:\/\/worker\.dev\/session\/sess-1\/res\/.+\/init\.mp4"/);
  assert.match(rewritten, /https:\/\/worker\.dev\/session\/sess-1\/res\/.+\/seg_001\.ts/);
  assert.match(rewritten, /https:\/\/worker\.dev\/session\/sess-1\/res\/.+\/seg_002\.ts/);
});

// ============================================================================
// PART C.4: RANGE REQUESTS & SEGMENT PROXYING
// ============================================================================

test("Worker: Nonexistent or expired session returns 410 Gone", async () => {
  const req = new Request("https://worker.dev/session/nonexistent-session-id/master.m3u8");
  const res = await worker.fetch(req, {});
  assert.strictEqual(res.status, 410);
  const json = await res.json();
  assert.strictEqual(json.error, "session_expired_or_not_found");
});

test("Worker: Health check returns 200 OK", async () => {
  const req = new Request("https://worker.dev/health");
  const res = await worker.fetch(req, {});
  assert.strictEqual(res.status, 200);
  const json = await res.json();
  assert.strictEqual(json.status, "ok");
});

test("Worker: CORS preflight returns 204 No Content with headers", async () => {
  const req = new Request("https://worker.dev/session/abc/master.m3u8", {
    method: "OPTIONS"
  });
  const res = await worker.fetch(req, {});
  assert.strictEqual(res.status, 204);
  assert.strictEqual(res.headers.get("Access-Control-Allow-Origin"), "*");
});

// ============================================================================
// PART C.5: FULL PLAYLIST REWRITING & MEDIA SEGMENT SERVING PIPELINE
// ============================================================================

test("E2E Pipeline: Master playlist -> Variant -> TS Segment with Range request", async () => {
  const env = { ALLOW_INSECURE_TEST_UPSTREAM: "true" };

  // 1. Create playback session pointing to mock upstream master playlist
  const createReq = new Request("https://worker-1.example.workers.dev/session", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      url: `${mockBaseUrl}/master.m3u8`,
      headers: {
        "Origin": "https://source.example.com",
        "Referer": "https://source.example.com/play"
      },
      videoId: "550",
      type: "movie"
    })
  });

  const createRes = await worker.fetch(createReq, env);
  assert.strictEqual(createRes.status, 201);
  const sessionInfo = await createRes.json();
  assert.strictEqual(sessionInfo.success, true);
  const playbackUrl = sessionInfo.playback_url;

  // 2. Fetch master playlist via client-facing URL
  const masterReq = new Request(playbackUrl);
  const masterRes = await worker.fetch(masterReq, env);
  assert.strictEqual(masterRes.status, 200);
  assert.match(masterRes.headers.get("Content-Type"), /application\/vnd\.apple\.mpegurl/);
  const masterText = await masterRes.text();

  // Verify multi-audio, subtitle, and variant URIs are all rewritten to worker domain
  assert.match(masterText, /#EXTM3U/);
  assert.match(masterText, /#EXT-X-MEDIA:TYPE=AUDIO.+URI="https:\/\/worker-1\.example\.workers\.dev\/session\//);
  assert.match(masterText, /#EXT-X-MEDIA:TYPE=SUBTITLES.+URI="https:\/\/worker-1\.example\.workers\.dev\/session\//);
  assert.match(masterText, /https:\/\/worker-1\.example\.workers\.dev\/session\/.+\/1080p\.m3u8/);

  // Extract rewritten 1080p variant URL
  const variantLine = masterText.split("\n").find(line => line.includes("1080p.m3u8"));
  assert.ok(variantLine, "Variant URL must be present in master playlist");

  // 3. Fetch nested variant playlist
  const variantReq = new Request(variantLine.trim());
  const variantRes = await worker.fetch(variantReq, env);
  assert.strictEqual(variantRes.status, 200);
  const variantText = await variantRes.text();

  // Verify encryption key, initialization map, and segments rewritten
  assert.match(variantText, /#EXT-X-KEY:METHOD=AES-128,URI="https:\/\/worker-1\.example\.workers\.dev\/session\//);
  assert.match(variantText, /#EXT-X-MAP:URI="https:\/\/worker-1\.example\.workers\.dev\/session\//);
  assert.match(variantText, /https:\/\/worker-1\.example\.workers\.dev\/session\/.+\/seg_001\.ts/);

  // Extract rewritten TS segment URL
  const segmentLine = variantText.split("\n").find(line => line.includes("seg_001.ts"));
  assert.ok(segmentLine, "Segment URL must be present in variant playlist");

  // 4. Fetch TS segment with byte-range request (bytes=0-99)
  const segmentReq = new Request(segmentLine.trim(), {
    headers: { "Range": "bytes=0-99" }
  });
  const segmentRes = await worker.fetch(segmentReq, env);
  assert.strictEqual(segmentRes.status, 206);
  assert.strictEqual(segmentRes.headers.get("Content-Type"), "video/mp2t");
  assert.strictEqual(segmentRes.headers.get("Content-Range"), "bytes 0-99/1024");
  assert.strictEqual(segmentRes.headers.get("Content-Length"), "100");
  assert.strictEqual(segmentRes.headers.get("Access-Control-Allow-Origin"), "*");

  const segmentBytes = await segmentRes.arrayBuffer();
  assert.strictEqual(segmentBytes.byteLength, 100);
});

test("E2E Pipeline: Server-side Origin and Referer applied to upstream", async () => {
  const env = { ALLOW_INSECURE_TEST_UPSTREAM: "true" };

  // Create session pointing to /check-headers
  const createReq = new Request("https://worker.dev/session", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      url: `${mockBaseUrl}/check-headers`,
      headers: {
        "Origin": "https://custom-origin.example.com",
        "Referer": "https://custom-referer.example.com/stream"
      }
    })
  });

  const createRes = await worker.fetch(createReq, env);
  const sessionInfo = await createRes.json();

  // Fetch the resource via worker
  const token = base64UrlEncode(`${mockBaseUrl}/check-headers`);
  const req = new Request(`https://worker.dev/session/${sessionInfo.session_id}/res/${token}/headers.json`);
  const res = await worker.fetch(req, env);
  assert.strictEqual(res.status, 200);

  const upstreamReceived = await res.json();
  assert.strictEqual(upstreamReceived.origin, "https://custom-origin.example.com");
  assert.strictEqual(upstreamReceived.referer, "https://custom-referer.example.com/stream");
});

test("Extractors: Unpack packed JavaScript de-obfuscation", async () => {
  const { unpackPackedJS } = await import("../src/extractors.js");
  const packedSample = `eval(function(p,a,c,k,e,d){while(c--)if(k[c])p=p.replace(new RegExp('\\\\b'+c.toString(a)+'\\\\b','g'),k[c]);return p}('1 0="3";',4,4,'src|var||https://cdn.example.com/master.m3u8'.split('|')))`;
  const unpacked = unpackPackedJS(packedSample);
  assert.match(unpacked, /var\s+src="https:\/\/cdn\.example\.com\/master\.m3u8"/);
});

test("Worker POST: /extract endpoint validates missing parameters", async () => {
  const env = { ALLOW_INSECURE_TEST_UPSTREAM: "true" };
  const req = new Request("https://worker.dev/extract", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider: "unknown", id: "123" })
  });
  const res = await worker.fetch(req, env);
  assert.strictEqual(res.status, 400);
  const data = await res.json();
  assert.strictEqual(data.error, "unsupported_provider");
});


