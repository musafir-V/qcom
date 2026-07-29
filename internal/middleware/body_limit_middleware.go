package middleware

import "net/http"

// DefaultMaxBodyBytes caps request bodies. Every endpoint takes small JSON —
// file content goes to S3 through presigned URLs, never through this service.
const DefaultMaxBodyBytes int64 = 1 << 20

// MaxBodyBytes rejects request bodies larger than limit.
func MaxBodyBytes(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}
