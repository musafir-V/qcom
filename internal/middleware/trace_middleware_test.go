package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/logging"
)

func TestTraceIDMiddleware_HeaderPrecedence(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"trace header wins", map[string]string{"X-Trace-Id": "trace-1", "X-Request-Id": "req-1"}, "trace-1"},
		{"falls back to request id", map[string]string{"X-Request-Id": "req-1"}, "req-1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen string
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = logging.TraceIDFromContext(r.Context())
			})
			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()

			TraceIDMiddleware(next).ServeHTTP(rec, req)

			if seen != tc.want {
				t.Fatalf("context trace id = %q, want %q", seen, tc.want)
			}
			if got := rec.Header().Get("X-Trace-Id"); got != tc.want {
				t.Fatalf("response header = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTraceIDMiddleware_GeneratesUUIDWhenAbsent(t *testing.T) {
	var seen string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = logging.TraceIDFromContext(r.Context())
	})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	TraceIDMiddleware(next).ServeHTTP(rec, req)

	if _, err := uuid.Parse(seen); err != nil {
		t.Fatalf("generated trace id %q is not a uuid: %v", seen, err)
	}
	if got := rec.Header().Get("X-Trace-Id"); got != seen {
		t.Fatalf("response header = %q, want %q", got, seen)
	}
}
