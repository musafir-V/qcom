package middleware

import (
	"crypto/subtle"
	"net/http"
)

// HeaderInternalAPIKey carries the shared secret for /internal/v1 routes.
const HeaderInternalAPIKey = "X-Internal-Api-Key"

// RequireInternalAPIKey gates service-to-service routes on a shared secret.
// An empty key disables the check, leaving the routes protected by network
// isolation alone (the pre-existing behaviour).
func RequireInternalAPIKey(key string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if key == "" || r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.Header.Get(HeaderInternalAPIKey)), []byte(key)) != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":{"code":"UNAUTHORIZED","message":"Invalid internal API key"}}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
