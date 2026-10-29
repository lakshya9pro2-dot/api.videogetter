# -*- coding: utf-8 -*-
import re
import socket
import time
import urllib.parse
import sys
import requests

# Mock xbmc logger
class MockXBMC:
    LOGINFO = 1
    LOGWARNING = 2
    LOGERROR = 3

    def log(self, msg, level=1):
        levels = {1: "[INFO]", 2: "[WARN]", 3: "[ERROR]"}
        print(f"{levels.get(level, '[LOG]')} {msg}")

xbmc = MockXBMC()

# Mock resolver_utils
class MockResolverUtils:
    session = requests.Session()

    @staticmethod
    def get_ua():
        return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

    @classmethod
    def http_get(cls, url, headers=None, retries=0, sleep=0, timeout=10.0):
        hdrs = {
            "User-Agent": cls.get_ua(),
            "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
        }
        if headers:
            hdrs.update(headers)
        try:
            resp = cls.session.get(url, headers=hdrs, timeout=timeout)
            if resp.status_code == 200:
                return resp.text
            else:
                xbmc.log(f"HTTP {resp.status_code} fetching {url}", xbmc.LOGWARNING)
                return ""
        except Exception as e:
            xbmc.log(f"Error fetching {url}: {e}", xbmc.LOGWARNING)
            return ""

resolver_utils = MockResolverUtils()

def _base_n(num, base):
    chars = "0123456789abcdefghijklmnopqrstuvwxyz"
    if num == 0:
        return "0"
    value = ""
    while num:
        num, rem = divmod(num, base)
        value = chars[rem] + value
    return value


def _unpack_packed_js(html):
    match = re.search(
        r"eval\(function\(p,a,c,k,e,d\)\{.*?\}\('([\s\S]*?)',(\d+),(\d+),'([\s\S]*?)'\.split\('\|'\)",
        html,
    )
    if not match:
        return ""

    payload = match.group(1).encode("utf-8").decode("unicode_escape")
    base = int(match.group(2))
    count = int(match.group(3))
    words = match.group(4).split("|")

    for index in range(count - 1, -1, -1):
        if index < len(words) and words[index]:
            payload = re.sub(r"\b" + re.escape(_base_n(index, base)) + r"\b", words[index], payload)
    return payload


def _hglink_to_hanerix(url):
    parsed = urllib.parse.urlparse(url)
    if parsed.netloc.lower() not in ("hglink.to", "www.hglink.to", "hgcloud.to", "www.hgcloud.to"):
        return url

    path = parsed.path
    if path.startswith("/e/"):
        return urllib.parse.urlunparse(("https", "hanerix.com", path, "", parsed.query, parsed.fragment))
    return url


def _streams_from_html(html, page_url):
    unpacked = _unpack_packed_js(html)
    if not unpacked:
        return []
    
    # Save unpacked for inspection
    with open("hanerix_test/unpacked.js", "w", encoding="utf-8") as f:
        f.write(unpacked)

    # Follow jwplayer setup priority: links.hls4 || links.hls3 || links.hls2
    links_match = re.search(r"var\s+links\s*=\s*\{([\s\S]*?)\};\s*jwplayer", unpacked)
    if links_match:
        links = {}
        for key, value in re.findall(r'"([^"]+)"\s*:\s*"([^"]*)"', links_match.group(1)):
            if value:
                links[key] = value.replace("\\/", "/")
        streams = [
            urllib.parse.urljoin(page_url, links[key])
            for key in ("hls4", "hls3", "hls2") if links.get(key)
        ]
        if streams:
            return streams

    # 2. Also check for sources: [...] in jwplayer setup
    sources_match = re.search(r"sources\s*:\s*\[([\s\S]*?)\]", unpacked)
    if sources_match:
        found_urls = re.findall(r'file\s*:\s*["\']([^"\']+)["\']', sources_match.group(1))
        if found_urls:
            return [urllib.parse.urljoin(page_url, u.replace("\\/", "/")) for u in found_urls]

    return []


_MAX_RESOLVE_SECONDS = 30.0
_REQUEST_TIMEOUT_SECONDS = 10.0


def _bounded_get(url, headers, deadline):
    remaining = deadline - time.monotonic()
    if remaining <= 0.5:
        return ""
    timeout = min(_REQUEST_TIMEOUT_SECONDS, max(5.0, remaining / 2.0))
    return resolver_utils.http_get(
        url, headers=headers, retries=0, sleep=0, timeout=timeout
    )


def _playlist_hosts_resolve(stream_url, headers, deadline):
    if not _host_resolves(stream_url):
        return False
    master = _bounded_get(stream_url, headers, deadline)
    if not master or "#EXTM3U" not in master[:64]:
        return False
    media_url = ""
    for line in master.splitlines():
        if line.strip() and not line.startswith("#"):
            media_url = urllib.parse.urljoin(stream_url, line.strip())
            break
    if not media_url or not _host_resolves(media_url):
        return False
    if "#EXT-X-STREAM-INF" not in master:
        return True
    return time.monotonic() < deadline


def _host_resolves(url):
    host = urllib.parse.urlparse(url).hostname
    if not host:
        return False
    try:
        socket.getaddrinfo(host, 443, socket.AF_UNSPEC, socket.SOCK_STREAM)
        return True
    except OSError:
        return False


def resolve(url, referer="", headers=None):
    xbmc.log(f"[AdultHideout][hglink] Resolving: {url}", xbmc.LOGINFO)

    page_url = _hglink_to_hanerix(url)
    request_headers = {
        "User-Agent": resolver_utils.get_ua(),
        "Referer": referer or "https://hglink.to/",
    }
    if headers:
        request_headers.update(headers)

    deadline = time.monotonic() + _MAX_RESOLVE_SECONDS
    page_html = _bounded_get(page_url, request_headers, deadline)
    if not page_html:
        xbmc.log(
            f"[AdultHideout][hglink] Player page unavailable; stopped within {_MAX_RESOLVE_SECONDS:.0f}s budget",
            xbmc.LOGWARNING,
        )
        return "", {}

    candidates = _streams_from_html(page_html, page_url)
    xbmc.log(f"[AdultHideout][hglink] Found {len(candidates)} stream candidate(s): {candidates}", xbmc.LOGINFO)

    for candidate in candidates:
        if time.monotonic() >= deadline:
            break
        if _playlist_hosts_resolve(candidate, request_headers, deadline):
            xbmc.log("[AdultHideout][hglink] Found reachable HLS playlist", xbmc.LOGINFO)
            return candidate, {
                "User-Agent": request_headers["User-Agent"],
                "Referer": page_url,
            }
        else:
            xbmc.log(f"[AdultHideout][hglink] Candidate {candidate} host/playlist check failed", xbmc.LOGWARNING)

    xbmc.log(
        f"[AdultHideout][hglink] No reachable HLS stream found within {min(_MAX_RESOLVE_SECONDS, time.monotonic() - (deadline - _MAX_RESOLVE_SECONDS)):.1f}s",
        xbmc.LOGWARNING,
    )
    return "", {}


if __name__ == "__main__":
    target = "https://hanerix.com/e/fenu7x8mzqb4"
    if len(sys.argv) > 1:
        target = sys.argv[1]
    
    print(f"=== TESTING HANERIX RESOLVER ===")
    print(f"Target: {target}\n")
    stream_url, headers = resolve(target)
    print("\n=== RESOLUTION RESULT ===")
    print(f"Stream URL : {stream_url}")
    print(f"Headers    : {headers}")
