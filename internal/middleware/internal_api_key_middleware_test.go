package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequireInternalAPIKey_EmptyKeyAllowsAll(t *testing.T) {
	rec := httptest.NewRecorder()
	RequireInternalAPIKey("")(okHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/internal/v1/x", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestRequireInternalAPIKey_RejectsWrongKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/x", nil)
	req.Header.Set(HeaderInternalAPIKey, "nope")
	rec := httptest.NewRecorder()
	RequireInternalAPIKey("secret")(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRequireInternalAPIKey_AcceptsMatchingKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/x", nil)
	req.Header.Set(HeaderInternalAPIKey, "secret")
	rec := httptest.NewRecorder()
	RequireInternalAPIKey("secret")(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestCORS_AllowlistEchoesOnlyKnownOrigins(t *testing.T) {
	mw := NewCORSMiddleware([]string{"https://admin.example.com"})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://admin.example.com")
	rec := httptest.NewRecorder()
	mw(okHandler()).ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://admin.example.com" {
		t.Fatalf("allow-origin = %q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec = httptest.NewRecorder()
	mw(okHandler()).ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("allow-origin = %q, want empty for unknown origin", got)
	}
}

func TestCORS_EmptyAllowlistKeepsWildcard(t *testing.T) {
	rec := httptest.NewRecorder()
	NewCORSMiddleware(nil)(okHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("allow-origin = %q, want *", got)
	}
}
