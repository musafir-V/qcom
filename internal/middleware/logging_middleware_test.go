package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

func TestLoggingMiddleware_LogsRequest(t *testing.T) {
	var buf bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&buf)
	logger.SetFormatter(&logrus.JSONFormatter{})

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/home", nil)
	req.RemoteAddr = "10.0.0.1:5555"
	req = req.WithContext(logging.WithTraceID(req.Context(), "trace-9"))
	rec := httptest.NewRecorder()

	LoggingMiddleware(logger)(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}

	var fields map[string]any
	if err := json.Unmarshal(buf.Bytes(), &fields); err != nil {
		t.Fatalf("log line %q is not json: %v", buf.String(), err)
	}
	want := map[string]any{
		"method":      http.MethodPost,
		"path":        "/api/v1/home",
		"status":      float64(http.StatusTeapot),
		"remote_addr": "10.0.0.1:5555",
		"trace_id":    "trace-9",
		"msg":         "HTTP request",
	}
	for k, v := range want {
		if fields[k] != v {
			t.Fatalf("log field %s = %v, want %v", k, fields[k], v)
		}
	}
	if _, ok := fields["duration"]; !ok {
		t.Fatal("expected a duration field")
	}
}

func TestLoggingMiddleware_DefaultsToStatus200(t *testing.T) {
	var buf bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&buf)
	logger.SetFormatter(&logrus.JSONFormatter{})

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	rec := httptest.NewRecorder()

	LoggingMiddleware(logger)(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	var fields map[string]any
	if err := json.Unmarshal(buf.Bytes(), &fields); err != nil {
		t.Fatalf("log line %q is not json: %v", buf.String(), err)
	}
	if fields["status"] != float64(http.StatusOK) {
		t.Fatalf("status = %v, want 200 when the handler never calls WriteHeader", fields["status"])
	}
}
