package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/metrics"
)

func scrape(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status = %d", rec.Code)
	}
	return rec.Body.String()
}

func TestRouteTemplate(t *testing.T) {
	router := mux.NewRouter()
	var got string
	router.HandleFunc("/api/v1/drivers/{phone}", func(w http.ResponseWriter, r *http.Request) {
		got = routeTemplate(r)
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/drivers/0971234567", nil))

	if got != "/api/v1/drivers/{phone}" {
		t.Fatalf("routeTemplate = %q, want the path template", got)
	}

	if unmatched := routeTemplate(httptest.NewRequest(http.MethodGet, "/nope", nil)); unmatched != "unmatched" {
		t.Fatalf("routeTemplate = %q, want %q for a request with no matched route", unmatched, "unmatched")
	}
}

func TestMetricsMiddleware_RecordsByRouteTemplate(t *testing.T) {
	router := mux.NewRouter()
	router.Use(MetricsMiddleware)
	router.HandleFunc("/api/v1/trips/{id}/metrics-test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/trips/t-1/metrics-test", nil))

	body := scrape(t)
	want := `http_request_duration_seconds_count{method="GET",route="/api/v1/trips/{id}/metrics-test",status="201"} 1`
	if !strings.Contains(body, want) {
		t.Fatalf("scrape does not contain %q:\n%s", want, filterLines(body, "metrics-test"))
	}
	if strings.Contains(body, `route="/api/v1/trips/t-1/metrics-test"`) {
		t.Fatal("raw path was used as a label, which would blow up label cardinality")
	}
}

func TestMetricsMiddleware_SkipsHealth(t *testing.T) {
	router := mux.NewRouter()
	router.Use(MetricsMiddleware)
	called := false
	router.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { called = true })

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))

	if !called {
		t.Fatal("expected the health handler to run")
	}
	if body := scrape(t); strings.Contains(body, `route="/health"`) {
		t.Fatal("health checks should not be recorded")
	}
}

func filterLines(body, substr string) string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, substr) {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
