# FilmU / ShowBox Stream Scraper & M3U Generator

## 1. Overview for AI Agents & Developers

This repository contains tools to query the **ShowBox scraping API** (`box.filmu.in`), extract direct video stream links (primarily HLS `.m3u8` manifests hosted on `shegu.net`), resolve necessary HTTP headers, and generate **Extended M3U playlists** compatible with media players (VLC, Kodi, mpv, IPTV Smarters, TiviMate).

---

## 2. API Specification (`box.filmu.in`)

### Minimal Endpoint Structure
```http
GET https://box.filmu.in/scrape/ShowBox/movie/{tmdb_id}?title={title}&febboxToken={jwt_token}
X-Api-Key: 09eb429913afb6b1cc90f23746f41fb3279aed77726c625c40672b81444c0bac
```

### Parameter & Header Matrix (Field Requirements)

| Field | Location | Required? | Description & Constraints |
| :--- | :--- | :---: | :--- |
| `{tmdb_id}` | URL Path | **YES** | The Movie Database ID (e.g., `550`, `19995`, `872585`). IMDb ID (`tt...`) is **NOT** needed. |
| `title` | Query Param | **YES** | Exact movie title (e.g., `Fight Club`, `Avatar`). **Omission returns `400 {"error": "?title= is required"}`**. |
| `febboxToken` | Query Param | **YES** | Febbox JWT user token. **Omission returns empty sources (`"sources": []`)**. |
| `X-Api-Key` | HTTP Header | **YES** | Static API key. **Omission returns `401 {"error": "Unauthorized"}`**. |
| `year` | Query Param | **NO** | Safe to omit. |
| `tmdbId` | Query Param | **NO** | Safe to omit when `{tmdb_id}` is already in the path. |
| Browser Headers | HTTP Headers | **NO** | `Host`, `User-Agent`, `Referer`, `Origin`, and `Sec-*` can all be omitted. |

---

## 3. Response Structure & URL Deconstruction

### Raw API Response Sample
```json
{
  "name": "ShowBox",
  "sources": [
    {
      "name": "ShowBox [8.09 GB]",
      "url": "https://wormhole.filmu.in/proxy/m3u8?url=https%3A%2F%2Fhls.shegu.net%2F38214555.m3u8%3Fsign%3D2575...&referer=https%3A%2F%2Fwww.febbox.com&origin=https%3A%2F%2Fwww.febbox.com&ua=Mozilla%2F5.0...",
      "quality": "4K",
      "type": "m3u8"
    }
  ],
  "subtitles": [],
  "cached": false
}
```

### How to Parse Each Source
1. **Source Type Filtering**:
   - Filter `source["type"] == "m3u8"` or URL containing `.m3u8`.
   - Exclude `.mp4`, `.mkv`, and container files if strictly seeking HLS streams.
2. **Direct Stream Extraction**:
   - The returned `url` is wrapped in `wormhole.filmu.in/proxy/...`.
   - Parse query parameters: `query_params = urllib.parse.parse_qs(urllib.parse.urlparse(proxy_url).query)`.
   - Extract and unquote `query_params['url'][0]` to obtain the true CDN URL (e.g. `https://hls.shegu.net/38214555.m3u8?...`).
3. **Required HTTP Headers for Playback**:
   The CDN will reject requests with `403 Forbidden` unless the following headers are passed:
   - `Referer`: `https://www.febbox.com` (extracted from `query_params['referer']`)
   - `Origin`: `https://www.febbox.com` (extracted from `query_params['origin']`)
   - `User-Agent`: Extracted from `query_params['ua']`

---

## 4. Codebase Reference

| File | Purpose | Key Details |
| :--- | :--- | :--- |
| [`make_m3u.py`](make_m3u.py) | **Main Generator Script** | Fetches sources, filters exclusively for `.m3u8`, sorts by quality/size, extracts playback headers, and writes a multi-player compatible Extended M3U playlist. |
| [`u.py`](u.py) | **Legacy / Reference Script** | Fetches Fight Club sources, extracts headers, and launches `mpv` directly with `--http-header-fields`. |
| [`playlist.m3u`](playlist.m3u) | **Sample Generated Playlist** | Example playlist containing Fight Club's 7 M3U8 streams. |

---

## 5. How to Run `make_m3u.py`

### Basic Execution (Fight Club default)
```bash
python3 make_m3u.py
```
Outputs to `playlist.m3u`.

### Query Any Movie by TMDB ID & Title
```bash
# Avatar (TMDB 19995)
python3 make_m3u.py --tmdb 19995 --title "Avatar" -o avatar.m3u

# Oppenheimer (TMDB 872585)
python3 make_m3u.py --tmdb 872585 --title "Oppenheimer" -o oppenheimer.m3u

# The Shawshank Redemption (TMDB 278)
python3 make_m3u.py --tmdb 278 --title "The Shawshank Redemption" -o shawshank.m3u
```

### CLI Flags Reference
- `--tmdb <ID>`: TMDB ID (default: `550`)
- `--title <STRING>`: Movie Title (default: `Fight Club`)
- `-o, --output <PATH>`: Destination `.m3u` file (default: `playlist.m3u`)
- `--no-sort`: Disable quality/size descending sort (keeps original API order)
- `--pipe`: Format stream URLs with pipe-syntax headers (`url|User-Agent=...&Referer=...`) for IPTV players requiring piped parameters

---

## 6. M3U Playlist Format Standards Implemented

The generated playlist outputs Extended M3U format with multi-player compatibility:



## 7. Instructions for AI When Modifying or Extending

1. **If adding TV Show scraping**:
   ShowBox scraper endpoints for series typically use `/scrape/ShowBox/tv/{tmdb_id}?season={s}&episode={e}&title={title}&febboxToken={token}`. Verify whether `season` and `episode` are required query parameters before querying.
2. **If adding auto-resolution for Movie Title**:
   Query a TMDB metadata provider or TMDB API using `{tmdb_id}` to resolve the `title` string automatically before calling `fetch_sources()`.
3. **If handling token expiration**:
   The `febboxToken` is a standard JWT with an `exp` claim (`1822805639`). When expired, refresh `FEBBOX_TOKEN` in the configuration.
