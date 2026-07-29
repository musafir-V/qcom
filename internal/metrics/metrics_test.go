package metrics

import (
	"math"
	"testing"
)

func TestObserveRequest(t *testing.T) {
	ObserveRequest("POST", "/api/v1/observe-test", 503, 0.42)
	ObserveRequest("POST", "/api/v1/observe-test", 503, 1.5)

	count, sum, ok := gatherHistogram(t, "http_request_duration_seconds", map[string]string{
		"method": "POST", "route": "/api/v1/observe-test", "status": "503",
	})
	if !ok {
		t.Fatal("no series recorded for the observed labels")
	}
	if count != 2 {
		t.Fatalf("sample count = %d, want 2", count)
	}
	if math.Abs(sum-1.92) > 1e-9 {
		t.Fatalf("sample sum = %v, want 1.92", sum)
	}
}

func TestObserveRequest_SeparateSeriesPerStatus(t *testing.T) {
	ObserveRequest("GET", "/api/v1/series-test", 200, 0.1)
	ObserveRequest("GET", "/api/v1/series-test", 500, 0.1)

	for _, status := range []string{"200", "500"} {
		count, _, ok := gatherHistogram(t, "http_request_duration_seconds", map[string]string{
			"method": "GET", "route": "/api/v1/series-test", "status": status,
		})
		if !ok || count != 1 {
			t.Fatalf("status %s: count = %d (found=%v), want 1", status, count, ok)
		}
	}
}

func TestHandler_ExposesQcomRegistry(t *testing.T) {
	ObserveRequest("GET", "/api/v1/handler-test", 200, 0.05)

	body := scrapeMetrics(t)
	for _, want := range []string{
		"# TYPE http_request_duration_seconds histogram",
		`http_request_duration_seconds_count{method="GET",route="/api/v1/handler-test",status="200"} 1`,
		"go_goroutines",
	} {
		if !containsLine(body, want) {
			t.Fatalf("scrape output missing %q", want)
		}
	}
}
