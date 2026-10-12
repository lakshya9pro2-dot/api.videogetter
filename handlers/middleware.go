package handlers

import (
	"net/http"
	"strings"
)

// IsM3URequest returns true if the request is for the M3U playlist side.
func IsM3URequest(r *http.Request) bool {
	path := strings.ToLower(r.URL.Path)

	if strings.Contains(path, ".m3u") ||
		strings.Contains(path, ".m3u8") ||
		strings.HasSuffix(path, "/playlist") ||
		path == "/playlist.m3u" ||
		path == "/api/playlist.m3u" ||
		path == "/playlist.m3u8" ||
		path == "/api/playlist.m3u8" ||
		path == "/hls.min.js" {
		return true
	}

	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "m3u" || format == "m3u8" {
		return true
	}

	return false
}

// IsAllowedOrigin checks if the Origin or Referer header matches the allowed origin.
func IsAllowedOrigin(origin, referer, allowed string) bool {
	cleanAllowed := strings.TrimRight(strings.TrimSpace(allowed), "/")
	if cleanAllowed == "" {
		cleanAllowed = "https://api.kineflex.site"
	}

	// 1. Check Origin
	cleanOrigin := strings.TrimRight(strings.TrimSpace(origin), "/")
	if cleanOrigin != "" {
		if strings.EqualFold(cleanOrigin, cleanAllowed) ||
			strings.EqualFold(cleanOrigin, "https://api.kineflex.site") ||
			strings.EqualFold(cleanOrigin, "http://api.kineflex.site") {
			return true
		}
	}

	// 2. Check Referer
	cleanReferer := strings.TrimSpace(referer)
	if cleanReferer != "" {
		lowerRef := strings.ToLower(cleanReferer)
		if strings.HasPrefix(lowerRef, strings.ToLower(cleanAllowed)) ||
			strings.HasPrefix(lowerRef, "https://api.kineflex.site") ||
			strings.HasPrefix(lowerRef, "http://api.kineflex.site") {
			return true
		}
	}

	return false
}

// IsValidAPIKey checks if the request includes the required secret API key
// via HTTP headers (X-Api-Key, Key, ApiKey, Authorization) or query parameters (?key=, ?api_key=).
func IsValidAPIKey(r *http.Request, requiredKey string) bool {
	if requiredKey == "" {
		return true
	}
	requiredKey = strings.TrimSpace(requiredKey)

	// Check common API key headers
	headersToCheck := []string{
		"X-Api-Key",
		"X-API-Key",
		"Key",
		"ApiKey",
		"Api-Key",
		"X-Key",
		"x-api-key",
		"key",
		"apikey",
	}
	for _, h := range headersToCheck {
		if val := strings.TrimSpace(r.Header.Get(h)); val != "" {
			if val == requiredKey {
				return true
			}
		}
	}

	// Check Authorization header (Bearer <key> or direct <key>)
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		if auth == requiredKey {
			return true
		}
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			token := strings.TrimSpace(auth[7:])
			if token == requiredKey {
				return true
			}
		}
	}

	// Check query parameters (?key=, ?api_key=, ?apiKey=, ?apikey=, ?k=)
	queryParams := []string{"key", "api_key", "apiKey", "apikey", "k"}
	for _, q := range queryParams {
		if val := strings.TrimSpace(r.URL.Query().Get(q)); val != "" {
			if val == requiredKey {
				return true
			}
		}
	}

	return false
}

// OriginFilterMiddleware enforces that:
// 1. Health checks (/health) pass unconditionally.
// 2. The M3U side (.m3u, .m3u8, ?format=m3u) is accessible without any Origin/Referer for all players.
// 3. All other backend endpoints require Origin/Referer from https://api.kineflex.site AND the secret key (lakshya9310081539@).
//    Unauthorized visitors/browsers are 302 redirected to https://api.kineflex.site/.
func OriginFilterMiddleware(next http.Handler, allowedOrigin, redirectURL, requiredKey string) http.Handler {
	if allowedOrigin == "" {
		allowedOrigin = "https://api.kineflex.site"
	}
	cleanAllowed := strings.TrimRight(strings.TrimSpace(allowedOrigin), "/")

	if redirectURL == "" {
		redirectURL = "https://api.kineflex.site/"
	}

	if requiredKey == "" {
		requiredKey = "lakshya9310081539@"
	}
	cleanKey := strings.TrimSpace(requiredKey)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Health check bypass
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		// 2. M3U side: Fully open to all players without origin or key
		if IsM3URequest(r) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, HEAD")
			w.Header().Set("Access-Control-Allow-Headers", "*")
			w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// 3. CORS Preflight (OPTIONS) for allowed origin
		if r.Method == http.MethodOptions && IsAllowedOrigin(r.Header.Get("Origin"), r.Header.Get("Referer"), cleanAllowed) {
			w.Header().Set("Access-Control-Allow-Origin", cleanAllowed)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, HEAD")
			w.Header().Set("Access-Control-Allow-Headers", "*")
			w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// 4. Main backend: ONLY respond if Origin/Referer is api.kineflex.site AND valid key is sent
		if IsAllowedOrigin(r.Header.Get("Origin"), r.Header.Get("Referer"), cleanAllowed) && IsValidAPIKey(r, cleanKey) {
			w.Header().Set("Access-Control-Allow-Origin", cleanAllowed)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, HEAD")
			w.Header().Set("Access-Control-Allow-Headers", "*")
			w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges")
			next.ServeHTTP(w, r)
			return
		}

		// 5. Unauthorized visitor to backend pages: Redirect to api.kineflex.site
		http.Redirect(w, r, redirectURL, http.StatusFound)
	})
}
