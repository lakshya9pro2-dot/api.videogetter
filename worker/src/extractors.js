/**
 * Extractors for Cloudflare Worker HLS/Media Proxy.
 * Enables in-worker extraction of IP-locked streaming services (StreamHG/Hanerix, Streamtape)
 * so extraction and playback proxying originate from the same Cloudflare edge network.
 */

/**
 * Base-N converter for Dean Edwards packed JavaScript de-obfuscation.
 *
 * @param {number} num
 * @param {number} base
 * @returns {string}
 */
export function baseN(num, base) {
  const chars = "0123456789abcdefghijklmnopqrstuvwxyz";
  if (num === 0) return "0";
  let val = "";
  while (num > 0) {
    const rem = num % base;
    val = chars[rem] + val;
    num = Math.floor(num / base);
  }
  return val;
}

/**
 * De-obfuscates Dean Edwards p,a,c,k,e,d JavaScript shells.
 *
 * @param {string} html
 * @returns {string}
 */
export function unpackPackedJS(html) {
  const match = html.match(
    /eval\(function\(p,a,c,k,e,d\)\{[\s\S]*?\}\('([\s\S]*?)',(\d+),(\d+),'([\s\S]*?)'\.split\('\|'\)/
  );
  if (!match) return "";

  let payload = match[1];
  try {
    payload = JSON.parse(`"${payload.replace(/"/g, '\\"')}"`);
  } catch (_) {
    payload = payload
      .replace(/\\'/g, "'")
      .replace(/\\"/g, '"')
      .replace(/\\\\/g, "\\");
  }

  const base = parseInt(match[2], 10);
  const count = parseInt(match[3], 10);
  const words = match[4].split("|");

  for (let i = count - 1; i >= 0; i--) {
    if (i < words.length && words[i]) {
      const tok = baseN(i, base);
      const re = new RegExp(`\\b${tok}\\b`, "g");
      payload = payload.replace(re, words[i]);
    }
  }
  return payload;
}

/**
 * Extracts streaming URL from StreamHG / Hanerix directly inside Cloudflare Worker.
 *
 * @param {string} urlOrId
 * @returns {Promise<{ streamUrl: string, streamType: string, title: string, headers: Record<string, string> }>}
 */
export async function extractStreamHG(urlOrId) {
  let fileCode = (urlOrId || "").trim();
  if (fileCode.includes("://")) {
    try {
      const u = new URL(fileCode);
      const parts = u.pathname.replace(/\/+$/, "").split("/");
      fileCode = parts[parts.length - 1];
    } catch (_) {}
  }
  fileCode = fileCode.replace(/[^a-zA-Z0-9_\-]/g, "");
  if (!fileCode) {
    throw new Error("Invalid or empty StreamHG ID");
  }

  const pageUrl = `https://hanerix.com/e/${fileCode}`;
  const resp = await fetch(pageUrl, {
    headers: {
      "User-Agent":
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
      "Referer": "https://hglink.to/",
      "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
    }
  });

  if (!resp.ok) {
    throw new Error(`Hanerix returned HTTP ${resp.status}`);
  }

  const html = await resp.text();
  let unpacked = unpackPackedJS(html);
  if (!unpacked) {
    const varSrcMatch = html.match(/var\s+src\s*=\s*"([^"]+)"/);
    if (varSrcMatch) {
      unpacked = `var links={"hls4":"${varSrcMatch[1]}"}; jwplayer`;
    }
  }

  if (!unpacked) {
    throw new Error("Could not unpack StreamHG JavaScript");
  }

  let streamUrl = "";
  const linksMatch = unpacked.match(/var\s+links\s*=\s*\{([\s\S]*?)\};\s*jwplayer/);
  if (linksMatch) {
    const links = {};
    const linkPairs = linksMatch[1].matchAll(/"([^"]+)"\s*:\s*"([^"]*)"/g);
    for (const p of linkPairs) {
      if (p[2]) links[p[1]] = p[2].replace(/\\\//g, "/");
    }
    // Respect JWPlayer fallback hierarchy: hls4 || hls3 || hls2
    for (const k of ["hls4", "hls3", "hls2"]) {
      if (links[k]) {
        streamUrl = links[k];
        break;
      }
    }
  }

  if (!streamUrl) {
    const sourcesMatch = unpacked.match(/sources\s*:\s*\[([\s\S]*?)\]/);
    if (sourcesMatch) {
      const fileMatch = sourcesMatch[1].match(/file\s*:\s*["']([^"']+)["']/);
      if (fileMatch && fileMatch[1]) {
        streamUrl = fileMatch[1].replace(/\\\//g, "/");
      }
    }
  }

  if (!streamUrl) {
    throw new Error("No playable HLS stream found in StreamHG page");
  }

  // Resolve relative URLs (e.g. /stream/...) against page URL
  const absoluteStreamUrl = new URL(streamUrl, pageUrl).href;

  let title = "";
  const titleMatch = html.match(/<title>(.*?)<\/title>/i);
  if (titleMatch) {
    title = titleMatch[1].trim();
  }

  return {
    streamUrl: absoluteStreamUrl,
    streamType: "m3u8",
    title,
    headers: {
      "Referer": pageUrl,
      "Origin": "https://hanerix.com"
    }
  };
}

/**
 * Extracts streaming URL from Streamtape directly inside Cloudflare Worker.
 *
 * @param {string} urlOrId
 * @returns {Promise<{ streamUrl: string, streamType: string, title: string, headers: Record<string, string> }>}
 */
export async function extractStreamtape(urlOrId) {
  let fileCode = (urlOrId || "").trim();
  if (fileCode.includes("://")) {
    try {
      const u = new URL(fileCode);
      const parts = u.pathname.replace(/\/+$/, "").split("/");
      fileCode = parts[parts.length - 1];
    } catch (_) {}
  }
  fileCode = fileCode.replace(/[^a-zA-Z0-9_\-]/g, "");
  if (!fileCode) {
    throw new Error("Invalid or empty Streamtape ID");
  }

  const pageUrl = `https://streamtape.com/v/${fileCode}/`;
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

  // Search for token expressions in script tags
  let matchExpr = "";
  const mRobot = html.match(/getElementById\(['"](?:no)?robotlink['"]\)\.innerHTML\s*=\s*([^;]+);/i);
  const mBot = html.match(/botlink['"]\)\.innerHTML\s*=\s*([^;]+);/i);
  const mIdeo = html.match(/getElementById\(['"]ideoooolink['"]\)\.innerHTML\s*=\s*([^;]+);/i);

  if (mRobot) matchExpr = mRobot[1].trim();
  else if (mBot) matchExpr = mBot[1].trim();
  else if (mIdeo) matchExpr = mIdeo[1].trim();

  if (!matchExpr) {
    throw new Error("Could not find Streamtape token expression in HTML");
  }

  // Evaluate substring expression
  let evalResult = "";
  try {
    const cleanExpr = matchExpr.replace(/[^a-zA-Z0-9_\-./?&=+()'"]/g, "");
    evalResult = new Function(`return (${cleanExpr})`)();
  } catch (_) {
    const tokenRegex = /['"]([^'"]*)['"]((?:\.substring\(\s*\d+\s*(?:,\s*\d+\s*)?\))*)/g;
    const parts = [];
    let tm;
    while ((tm = tokenRegex.exec(matchExpr)) !== null) {
      let val = tm[1];
      const subs = tm[2];
      const subCalls = [...subs.matchAll(/\.substring\((\d+)(?:,\s*(\d+))?\)/g)];
      for (const sc of subCalls) {
        const start = parseInt(sc[1], 10);
        const end = sc[2] ? parseInt(sc[2], 10) : val.length;
        val = val.substring(start, end);
      }
      parts.push(val);
    }
    evalResult = parts.join("");
  }

  if (!evalResult) {
    throw new Error("Failed to evaluate Streamtape token expression");
  }

  let getVideoUrl = evalResult;
  if (getVideoUrl.startsWith("//")) {
    getVideoUrl = "https:" + getVideoUrl;
  } else if (!getVideoUrl.startsWith("http")) {
    getVideoUrl = "https://" + getVideoUrl;
  }
  if (!getVideoUrl.includes("&stream=1")) {
    getVideoUrl += "&stream=1";
  }

  // Follow 302 redirect from Cloudflare Worker to resolve tapecontent.net direct URL
  let finalStreamUrl = getVideoUrl;
  try {
    const headResp = await fetch(getVideoUrl, {
      method: "HEAD",
      redirect: "manual",
      headers: {
        "User-Agent":
          "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
        "Referer": "https://streamtape.com/"
      }
    });

    if (headResp.status >= 300 && headResp.status < 400) {
      const loc = headResp.headers.get("Location") || headResp.headers.get("location");
      if (loc) {
        finalStreamUrl = loc;
      }
    }
  } catch (_) {}

  let title = "";
  const titleMatch = html.match(/<title>(.*?)<\/title>/i);
  if (titleMatch) {
    title = titleMatch[1].replace(/\s+at\s+Streamtape\.com.*$/i, "").trim();
  }

  return {
    streamUrl: finalStreamUrl,
    streamType: "mp4",
    title,
    headers: {
      "Referer": "https://streamtape.com/",
      "Origin": "https://streamtape.com"
    }
  };
}
