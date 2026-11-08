/**
 * Standalone Local Dev Server for Netlify Serverless Proxy.
 * Allows instant local testing without needing Netlify CLI.
 *
 * Usage:
 *   node server.mjs
 *   PORT=8888 node server.mjs
 */

import http from "node:http";
import handler from "./src/index.mjs";

const PORT = parseInt(process.env.PORT || "8888", 10);
const HOST = process.env.HOST || "0.0.0.0";

const server = http.createServer(async (req, res) => {
  try {
    const origin = `http://${req.headers.host || "localhost:" + PORT}`;
    const url = new URL(req.url, origin);

    const headers = new Headers();
    for (const [k, v] of Object.entries(req.headers)) {
      if (v !== undefined) {
        headers.set(k, Array.isArray(v) ? v.join(", ") : v);
      }
    }

    let body = null;
    if (req.method !== "GET" && req.method !== "HEAD") {
      const chunks = [];
      for await (const chunk of req) {
        chunks.push(chunk);
      }
      if (chunks.length > 0) {
        body = Buffer.concat(chunks);
      }
    }

    const request = new Request(url.href, {
      method: req.method,
      headers,
      body: body ? body : undefined,
      duplex: "half"
    });

    const response = await handler.fetch(request, process.env, {});

    res.statusCode = response.status;
    for (const [k, v] of response.headers.entries()) {
      res.setHeader(k, v);
    }

    if (response.body && req.method !== "HEAD") {
      const reader = response.body.getReader();
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        res.write(value);
      }
    }
    res.end();
  } catch (err) {
    if (!res.headersSent) {
      res.statusCode = 500;
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ error: "server_error", message: err.message }));
    }
  }
});

server.listen(PORT, HOST, () => {
  console.log(`[*] Netlify Serverless Proxy local test server listening on http://${HOST}:${PORT}`);
  console.log(`[*] Health check: http://127.0.0.1:${PORT}/health`);
  console.log(`[*] Streamtape extract: http://127.0.0.1:${PORT}/extract/st/0A2vDYQz3wIbPQ6`);
});
