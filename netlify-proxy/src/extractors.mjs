/**
 * In-worker / In-serverless Extractors for StreamHG and Streamtape.
 *
 * Provides same-server extraction and direct playback session creation,
 * eliminating IP/ASN binding mismatches for protected streaming providers.
 */

const DEFAULT_STREAMHG_BASE = "https://hanerix.com";

/**
 * Extracts playable HLS master.m3u8 stream from StreamHG / Hanerix.
 *
 * @param {string} fileCodeOrUrl - Embed code or full stream URL
 * @returns {Promise<{ streamUrl: string, streamType: string, title?: string, headers: Record<string, string> }>}
 */
export async function extractStreamHG(fileCodeOrUrl) {
  let fileCode = String(fileCodeOrUrl).trim();
  let embedUrl = "";

  if (fileCode.startsWith("http://") || fileCode.startsWith("https://")) {
    embedUrl = fileCode;
    const m = fileCode.match(/\/(?:e|v|embed)\/([a-zA-Z0-9_\-]+)/i);
    if (m) {
      fileCode = m[1];
    }
  } else {
    fileCode = fileCode.replace(/[^a-zA-Z0-9_\-]/g, "");
    if (!fileCode) {
      throw new Error("Invalid or empty StreamHG ID");
    }
    embedUrl = `${DEFAULT_STREAMHG_BASE}/e/${fileCode}`;
  }

  const embedOrigin = new URL(embedUrl).origin;

  const resp = await fetch(embedUrl, {
    headers: {
      "User-Agent":
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
      "Referer": embedOrigin + "/",
      "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
    }
  });

  if (!resp.ok) {
    throw new Error(`StreamHG returned HTTP ${resp.status}`);
  }

  const html = await resp.text();

  // 1. Direct var src = "..." check
  const varSrcMatch = html.match(/var\s+src\s*=\s*"([^"]+)"/i);
  if (varSrcMatch && varSrcMatch[1]) {
    let streamUrl = varSrcMatch[1];
    if (streamUrl.startsWith("//")) streamUrl = "https:" + streamUrl;
    return {
      streamUrl,
      streamType: streamUrl.includes(".mp4") ? "mp4" : "hls",
      headers: {
        "Referer": embedUrl,
        "Origin": embedOrigin
      }
    };
  }

  // 2. Packed JS unpack
  let unpacked = html;
  const packedMatch = html.match(
    /eval\(function\(p,a,c,k,e,d\)\{.*?\}\('([\s\S]*?)',(\d+),(\d+),'([\s\S]*?)'\.split\('\|'\)/
  );
  if (packedMatch) {
    unpacked = solvePackerJS(packedMatch[1], parseInt(packedMatch[2], 10), parseInt(packedMatch[3], 10), packedMatch[4].split("|"));
  }

  // 3. Search jwplayer / sources in unpacked text
  const sourcesMatch = unpacked.match(/sources\s*:\s*\[([\s\S]*?)\]/i);
  if (sourcesMatch) {
    const fileMatch = sourcesMatch[1].match(/file\s*:\s*["']([^"']+)["']/i);
    if (fileMatch) {
      let streamUrl = fileMatch[1];
      if (streamUrl.startsWith("//")) streamUrl = "https:" + streamUrl;
      return {
        streamUrl,
        streamType: streamUrl.includes(".mp4") ? "mp4" : "hls",
        headers: {
          "Referer": embedUrl,
          "Origin": embedOrigin
        }
      };
    }
  }

  // 4. Fallback search for any .m3u8 link in html or unpacked
  const m3u8Match = (unpacked + html).match(/["'](https?:\/\/[^"']+\.m3u8[^"']*)["']/i);
  if (m3u8Match) {
    return {
      streamUrl: m3u8Match[1],
      streamType: "hls",
      headers: {
        "Referer": embedUrl,
        "Origin": embedOrigin
      }
    };
  }

  throw new Error("No playable HLS or MP4 stream found in StreamHG embed");
}

/**
 * Extracts playable MP4 video URL from Streamtape.
 *
 * @param {string} fileCodeOrUrl - Streamtape file code or full page URL
 * @returns {Promise<{ streamUrl: string, streamType: string, title?: string, headers: Record<string, string> }>}
 */
export async function extractStreamtape(fileCodeOrUrl) {
  let fileCode = String(fileCodeOrUrl).trim();
  let pageUrl = "";

  if (fileCode.startsWith("http://") || fileCode.startsWith("https://")) {
    pageUrl = fileCode;
    const m = fileCode.match(/\/(?:v|e)\/([a-zA-Z0-9_\-]+)/i);
    if (m) {
      fileCode = m[1];
    }
  } else {
    fileCode = fileCode.replace(/[^a-zA-Z0-9_\-]/g, "");
    if (!fileCode) {
      throw new Error("Invalid or empty Streamtape ID");
    }
    pageUrl = `https://streamtape.com/v/${fileCode}/`;
  }

  const resp = await fetch(pageUrl, {
    headers: {
      "User-Agent":
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
      "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
    }
  });

  if (!resp.ok) {
    throw new Error(`Streamtape returned HTTP ${resp.status}`);
  }

  const html = await resp.text();

  // Streamtape CloudStream 3 token extraction
  let matchExpr = "";
  for (const line of html.split("\n")) {
    if (line.includes("botlink').innerHTML")) {
      const idx = line.indexOf(").innerHTML");
      if (idx !== -1) {
        const after = line.substring(idx + ").innerHTML".length);
        const eqIdx = after.indexOf("=");
        if (eqIdx !== -1) {
          let rawExpr = after.substring(eqIdx + 1);
          if (rawExpr.includes("</script>")) {
            rawExpr = rawExpr.substring(0, rawExpr.indexOf("</script>"));
          }
          matchExpr = rawExpr.trim().replace(/;+$/, "");
          break;
        }
      }
    }
  }

  if (!matchExpr) {
    const mRobot = html.match(/getElementById\(['"](?:no)?robotlink['"]\)\.innerHTML\s*=\s*([^;]+);/i);
    const mBot = html.match(/botlink['"]\)\.innerHTML\s*=\s*([^;]+);/i);
    const mIdeo = html.match(/getElementById\(['"]ideoooolink['"]\)\.innerHTML\s*=\s*([^;]+);/i);
    if (mRobot) matchExpr = mRobot[1].trim();
    else if (mBot) matchExpr = mBot[1].trim();
    else if (mIdeo) matchExpr = mIdeo[1].trim();
  }

  if (!matchExpr) {
    throw new Error("Could not find Streamtape token expression in HTML");
  }

  let evalResult = evaluateStreamtapeExpression(matchExpr);

  if (!evalResult) {
    try {
      const cleanExpr = matchExpr.replace(/[^a-zA-Z0-9_\-./?&=+()'"]/g, "");
      evalResult = new Function(`return (${cleanExpr})`)();
    } catch (_) {}
  }

  if (!evalResult) {
    throw new Error("Failed to evaluate Streamtape token expression");
  }

  let getVideoUrl = String(evalResult).trim();
  getVideoUrl = getVideoUrl
    .replace(/\/get_xcdvideo\?/i, "/get_video?")
    .replace(/xcdd=/i, "d=")
    .replace(/\/get_xcd/i, "/get_");

  if (getVideoUrl.startsWith("//")) {
    getVideoUrl = "https:" + getVideoUrl;
  } else if (!getVideoUrl.startsWith("http")) {
    getVideoUrl = "https://" + getVideoUrl;
  }
  if (!getVideoUrl.includes("&stream=1")) {
    getVideoUrl += "&stream=1";
  }

  let title = "";
  const titleMatch = html.match(/<title>(.*?)<\/title>/i);
  if (titleMatch) {
    title = titleMatch[1].replace(/\s+at\s+Streamtape\.com.*$/i, "").trim();
  }

  let streamOrigin = "https://streamtape.com";
  try {
    streamOrigin = new URL(pageUrl).origin;
  } catch (_) {}

  // Resolve 302 redirect from streamtape.com/get_video to the real CDN storage URL (tapecontent.net)
  let realStreamUrl = getVideoUrl;
  try {
    const redirResp = await fetch(getVideoUrl, {
      method: "GET",
      headers: {
        "User-Agent":
          "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
        "Referer": pageUrl,
        "Range": "bytes=0-0"
      },
      redirect: "manual"
    });

    if (redirResp.status >= 300 && redirResp.status < 400) {
      const loc = redirResp.headers.get("Location") || redirResp.headers.get("location");
      if (loc && (loc.startsWith("http://") || loc.startsWith("https://"))) {
        realStreamUrl = loc;
      }
    }
  } catch (err) {
    // If redirect check fails, realStreamUrl remains getVideoUrl
  }

  return {
    streamUrl: realStreamUrl,
    directUrl: realStreamUrl,
    tapecontentUrl: realStreamUrl,
    getVideoUrl: getVideoUrl,
    streamType: "mp4",
    title,
    headers: {
      "Referer": pageUrl,
      "Origin": streamOrigin
    }
  };
}

/**
 * Safely evaluates Streamtape token string concatenation and substring expressions without using eval().
 */
export function evaluateStreamtapeExpression(expr) {
  if (!expr || typeof expr !== "string") return null;

  try {
    const parts = expr.split("+");
    let result = "";

    for (let part of parts) {
      part = part.trim();
      if (!part) continue;

      const strMatch = part.match(/^\(?\s*['"]([\s\S]*?)['"]\s*\)?(.*)$/);
      if (strMatch) {
        let text = strMatch[1];
        const chain = strMatch[2].trim();

        if (chain) {
          const methodMatches = chain.matchAll(/\.(substring|slice)\(\s*(\d+)(?:\s*,\s*(\d+))?\s*\)/g);
          for (const m of methodMatches) {
            const method = m[1];
            const start = parseInt(m[2], 10);
            const end = m[3] !== undefined ? parseInt(m[3], 10) : undefined;

            if (method === "substring") {
              text = end !== undefined ? text.substring(start, end) : text.substring(start);
            } else if (method === "slice") {
              text = end !== undefined ? text.slice(start, end) : text.slice(start);
            }
          }
        }
        result += text;
      }
    }

    return result || null;
  } catch (_) {
    return null;
  }
}

/**
 * Standard Dean Edwards Packer JS unpacker.
 */
function solvePackerJS(payload, radix, count, words) {
  function getWord(idx) {
    return words[idx] || baseEncode(idx, radix);
  }

  function baseEncode(c, a) {
    return (
      (c < a ? "" : baseEncode(Math.floor(c / a), a)) +
      ((c = c % a) > 35 ? String.fromCharCode(c + 29) : c.toString(36))
    );
  }

  const dict = {};
  for (let i = count - 1; i >= 0; i--) {
    const key = baseEncode(i, radix);
    dict[key] = words[i] || key;
  }

  return payload.replace(/\b\w+\b/g, word => {
    return dict[word] !== undefined ? dict[word] : word;
  });
}
