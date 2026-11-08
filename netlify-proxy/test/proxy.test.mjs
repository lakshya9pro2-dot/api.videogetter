import { test, describe, before, after } from "node:test";
import assert from "node:assert";
import http from "node:http";
import handler from "../src/index.mjs";

describe("Netlify Serverless Proxy Suite", () => {
  let mockServer;
  let mockBaseUrl;

  before(async () => {
    process.env.NODE_ENV = "test";
    process.env.ALLOW_INSECURE_TEST_UPSTREAM = "true";

    mockServer = http.createServer((req, res) => {
      const url = new URL(req.url, `http://${req.headers.host}`);

      if (url.pathname === "/test.m3u8") {
        res.writeHead(200, { "Content-Type": "application/vnd.apple.mpegurl" });
        res.end(`#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:10
#EXTINF:10.0,
segment1.ts
#EXTINF:10.0,
https://example.com/segment2.ts
#EXT-X-ENDLIST`);
        return;
      }

      if (url.pathname === "/video.mp4") {
        const range = req.headers.range;
        if (range === "bytes=0-99") {
          res.writeHead(206, {
            "Content-Type": "video/mp4",
            "Content-Range": "bytes 0-99/1000",
            "Content-Length": "100",
            "Accept-Ranges": "bytes"
          });
          res.end(Buffer.alloc(100, 0x00));
          return;
        }

        res.writeHead(200, {
          "Content-Type": "video/mp4",
          "Content-Length": "1000",
          "Accept-Ranges": "bytes"
        });
        res.end(Buffer.alloc(1000, 0x00));
        return;
      }

      res.writeHead(404);
      res.end();
    });

    await new Promise(resolve => mockServer.listen(0, "127.0.0.1", resolve));
    const port = mockServer.address().port;
    mockBaseUrl = `http://127.0.0.1:${port}`;
  });

  after(() => {
    if (mockServer) mockServer.close();
  });

  test("GET /health returns 200 ok", async () => {
    const req = new Request("http://localhost:8888/health", { method: "GET" });
    const res = await handler.fetch(req, process.env);
    assert.strictEqual(res.status, 200);
    const data = await res.json();
    assert.strictEqual(data.status, "ok");
  });

  test("OPTIONS / preflight returns 204 with CORS headers", async () => {
    const req = new Request("http://localhost:8888/session/test/video.mp4", { method: "OPTIONS" });
    const res = await handler.fetch(req, process.env);
    assert.strictEqual(res.status, 204);
    assert.strictEqual(res.headers.get("Access-Control-Allow-Origin"), "*");
    assert.ok(res.headers.get("Access-Control-Allow-Methods").includes("GET"));
  });

  test("POST /session creates session and returns playback_url", async () => {
    const req = new Request("http://localhost:8888/session", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        url: `${mockBaseUrl}/test.m3u8`,
        headers: { Referer: "https://example.com" }
      })
    });

    const res = await handler.fetch(req, process.env);
    assert.strictEqual(res.status, 201);
    const data = await res.json();
    assert.strictEqual(data.success, true);
    assert.ok(data.session_id);
    assert.ok(data.playback_url.includes("/session/"));
    assert.ok(data.playback_url.includes("/master.m3u8"));
  });

  test("GET /session/:id/master.m3u8 fetches and rewrites playlist", async () => {
    const createReq = new Request("http://localhost:8888/session", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        url: `${mockBaseUrl}/test.m3u8`
      })
    });
    const createRes = await handler.fetch(createReq, process.env);
    const createData = await createRes.json();

    const getReq = new Request(createData.playback_url, { method: "GET" });
    const getRes = await handler.fetch(getReq, process.env);
    assert.strictEqual(getRes.status, 200);
    const text = await getRes.text();
    assert.ok(text.includes("#EXTM3U"));
    assert.ok(text.includes("/res/"));
  });

  test("GET /session/:id/video.mp4 with Range header returns 206 Partial Content", async () => {
    const createReq = new Request("http://localhost:8888/session", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        url: `${mockBaseUrl}/video.mp4`
      })
    });
    const createRes = await handler.fetch(createReq, process.env);
    const createData = await createRes.json();

    const rangeReq = new Request(createData.playback_url, {
      method: "GET",
      headers: { Range: "bytes=0-99" }
    });
    const rangeRes = await handler.fetch(rangeReq, process.env);
    assert.strictEqual(rangeRes.status, 206);
    assert.strictEqual(rangeRes.headers.get("Content-Range"), "bytes 0-99/1000");
    assert.strictEqual(rangeRes.headers.get("Content-Length"), "100");
  });

  test("Streamtape live extraction and 206 Partial Content range streaming", async () => {
    // 1. Extract Streamtape 0A2vDYQz3wIbPQ6 (Nilakanta TMDB 1107009)
    const extractReq = new Request("http://localhost:8888/extract/st/0A2vDYQz3wIbPQ6", {
      method: "GET"
    });
    const extractRes = await handler.fetch(extractReq, process.env);
    assert.strictEqual(extractRes.status, 200);
    const data = await extractRes.json();
    assert.strictEqual(data.success, true);
    assert.ok(data.playback_url);
    assert.strictEqual(data.stream_type, "mp4");

    // 2. Fetch video.mp4 with Range: bytes=0-100
    const videoReq = new Request(data.playback_url, {
      method: "GET",
      headers: { Range: "bytes=0-100" }
    });
    const videoRes = await handler.fetch(videoReq, process.env);
    assert.strictEqual(videoRes.status, 206);
    assert.strictEqual(videoRes.headers.get("Content-Type"), "video/mp4");
    assert.ok(videoRes.headers.get("Content-Range").startsWith("bytes 0-100/"));
    assert.strictEqual(videoRes.headers.get("Content-Length"), "101");
  });
});
