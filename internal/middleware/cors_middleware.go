package middleware

import (
	"net/http"
	"strings"
)

const corsAllowedHeaders = "Content-Type, Authorization, X-User-Category, X-Admin-Key"
const corsAllowedMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"

// CORSMiddleware allows any origin. Kept for callers that have no allowlist
// configured; prefer NewCORSMiddleware with CORS_ALLOWED_ORIGINS set.
var CORSMiddleware = NewCORSMiddleware(nil)

// NewCORSMiddleware echoes back the request Origin when it is in allowedOrigins.
// An empty allowlist preserves the legacy wildcard behaviour.
func NewCORSMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[strings.ToLower(o)] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(allowed) == 0 {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else {
				w.Header().Add("Vary", "Origin")
				origin := r.Header.Get("Origin")
				if _, ok := allowed[strings.ToLower(origin)]; ok {
					w.Header().Set("Access-Control-Allow-Origin", origin)
				}
			}
			w.Header().Set("Access-Control-Allow-Methods", corsAllowedMethods)
			w.Header().Set("Access-Control-Allow-Headers", corsAllowedHeaders)
			w.Header().Set("Access-Control-Max-Age", "3600")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusOK)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
