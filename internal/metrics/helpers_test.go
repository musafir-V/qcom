package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
)

// gatherHistogram returns the sample count and sum of the histogram series in
// name whose labels match every entry of want, or ok=false if no such series
// has been observed yet.
func gatherHistogram(t *testing.T, name string, want map[string]string) (count uint64, sum float64, ok bool) {
	t.Helper()
	for _, m := range gatherMetric(t, name) {
		if !labelsMatch(m, want) {
			continue
		}
		h := m.GetHistogram()
		return h.GetSampleCount(), h.GetSampleSum(), true
	}
	return 0, 0, false
}

// gatherValue returns the gauge or counter value of the series in name whose
// labels match every entry of want.
func gatherValue(t *testing.T, name string, want map[string]string) (float64, bool) {
	t.Helper()
	for _, m := range gatherMetric(t, name) {
		if !labelsMatch(m, want) {
			continue
		}
		if g := m.GetGauge(); g != nil {
			return g.GetValue(), true
		}
		if c := m.GetCounter(); c != nil {
			return c.GetValue(), true
		}
	}
	return 0, false
}

func gatherMetric(t *testing.T, name string) []*dto.Metric {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() == name {
			return f.GetMetric()
		}
	}
	return nil
}

func labelsMatch(m *dto.Metric, want map[string]string) bool {
	got := make(map[string]string, len(m.GetLabel()))
	for _, l := range m.GetLabel() {
		got[l.GetName()] = l.GetValue()
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

func scrapeMetrics(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status = %d", rec.Code)
	}
	return rec.Body.String()
}

func containsLine(body, substr string) bool {
	return strings.Contains(body, substr)
}
