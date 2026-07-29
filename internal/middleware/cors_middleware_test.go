package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSMiddleware_SetsHeaders(t *testing.T) {
	next := &okHandler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	rec := httptest.NewRecorder()

	CORSMiddleware(next).ServeHTTP(rec, req)

	if !next.called {
		t.Fatal("expected next handler to be called")
	}
	want := map[string]string{
		"Access-Control-Allow-Origin":  "*",
		"Access-Control-Allow-Methods": "GET, POST, PUT, PATCH, DELETE, OPTIONS",
		"Access-Control-Allow-Headers": "Content-Type, Authorization, X-User-Category, X-Admin-Key",
		"Access-Control-Max-Age":       "3600",
	}
	for header, value := range want {
		if got := rec.Header().Get(header); got != value {
			t.Fatalf("%s = %q, want %q", header, got, value)
		}
	}
}

func TestCORSMiddleware_ShortCircuitsPreflight(t *testing.T) {
	next := &okHandler{}
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/me", nil)
	rec := httptest.NewRecorder()

	CORSMiddleware(next).ServeHTTP(rec, req)

	if next.called {
		t.Fatal("expected preflight not to reach next handler")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("preflight response missing CORS headers, origin = %q", got)
	}
}
