import re
import subprocess
import urllib.parse
import requests

# 1. API endpoint and params
url = "https://box.filmu.in/scrape/ShowBox/movie/tt0137523"
params = {
    "tmdbId": "550",
    "title": "Fight Club",
    "year": "1999",
    "febboxToken": (
        "eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzI1NiJ9.eyJpYXQiOjE3OTE3MDE2MTksIm5iZiI6MTc5MTcwMTYxOSwiZXhwIjoxODIyODA1NjM5LCJkYXRhIjp7InVpZCI6MjM1NjE4OCwidG9rZW4iOiI4NDgxZDZmZDU1YWFjMzE0NmM4YmVhYjc0MDk3ZjIyMiJ9fQ.3XnZ66gABz4BnBzwuF1Pbpt-EqNX3y3UQ41JKB50gl4"
    ),
}

headers = {
    "Host": "box.filmu.in",
    "User-Agent": (
        "Mozilla/5.0 (X11; Linux x86_64; rv:151.0) Gecko/20100101"
        " Firefox/151.0"
    ),
    "Accept": "*/*",
    "Accept-Language": "en-US,en;q=0.9",
    "Referer": "https://embed.filmu.in/",
    "X-Api-Key": (
        "09eb429913afb6b1cc90f23746f41fb3279aed77726c625c40672b81444c0bac"
    ),
    "Origin": "https://embed.filmu.in",
    "Sec-GPC": "1",
    "Sec-Fetch-Dest": "empty",
    "Sec-Fetch-Mode": "cors",
    "Sec-Fetch-Site": "same-site",
    "Priority": "u=0",
    "Te": "trailers",
}


def parse_size_in_gb(name):
  """Extracts file size from name string and converts to GB for comparison."""
  match = re.search(r"\[([\d.]+)\s*(GB|MB)\]", name, re.IGNORECASE)
  if not match:
    return 0.0
  val = float(match.group(1))
  unit = match.group(2).upper()
  return val if unit == "GB" else val / 1024.0


def get_quality_rank(quality):
  """Assigns a numeric score to video qualities."""
  q = quality.lower()
  if "4k" in q or "2160p" in q:
    return 4
  if "1080p" in q:
    return 3
  if "720p" in q:
    return 2
  if "360p" in q:
    return 1
  return 0


def main():
  print("[*] Fetching stream list from API...")
  response = requests.get(url, params=params, headers=headers)

  if response.status_code != 200:
    print(
        f"[!] Error: Received status code {response.status_code} -"
        f" {response.text}"
    )
    return

  data = response.json()
  sources = data.get("sources", [])

  if not sources:
    print("[!] No sources found in response.")
    return

  # Sort sources by Quality Rank (descending) then by Size in GB (descending)
  best_source = max(
      sources,
      key=lambda s: (
          get_quality_rank(s.get("quality", "")),
          parse_size_in_gb(s.get("name", "")),
      ),
  )

  print(
      f"[+] Selected Stream: {best_source.get('name')} | Quality:"
      f" {best_source.get('quality')} | Type: {best_source.get('type')}"
  )

  proxy_url = best_source.get("url", "")

  # Parse query parameters from proxy URL
  parsed_url = urllib.parse.urlparse(proxy_url)
  query_params = urllib.parse.parse_qs(parsed_url.query)

  # Extract original stream URL
  inner_url = query_params.get("url", [None])[0]
  if inner_url:
    stream_url = urllib.parse.unquote(inner_url)
  else:
    stream_url = proxy_url

  # Extract embedded headers
  referer = query_params.get("referer", ["https://www.febbox.com"])[0]
  origin = query_params.get("origin", ["https://www.febbox.com"])[0]
  user_agent = query_params.get(
      "ua",
      [
          "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
          " (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
      ],
  )[0]

  # Format headers array for mpv
  mpv_headers = [
      f"Referer: {referer}",
      f"Origin: {origin}",
      f"User-Agent: {user_agent}",
  ]

  # Construct mpv command
  mpv_command = [
      "mpv",
      f"--http-header-fields={','.join(mpv_headers)}",
      stream_url,
  ]

  print("\n[*] Launching MPV with the following details:")
  print(f"URL: {stream_url}")
  print(f"Origin: {origin}")
  print(f"Referer: {referer}")
  print(f"User-Agent: {user_agent}\n")

  try:
    subprocess.run(mpv_command, check=True)
  except FileNotFoundError:
    print("[!] Error: 'mpv' executable not found. Make sure mpv is installed.")
  except subprocess.CalledProcessError as e:
    print(f"[!] MPV exited with error code {e.returncode}")


if __name__ == "__main__":
  main()