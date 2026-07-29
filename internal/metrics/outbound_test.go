package metrics

import (
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type stubTransport struct {
	resp *http.Response
	err  error
	seen *http.Request
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.seen = req
	return s.resp, s.err
}

func TestObserveExternalRequest(t *testing.T) {
	ObserveExternalRequest("observe_dep", "GET", "200", 0.25)
	ObserveExternalRequest("observe_dep", "GET", "200", 0.75)

	count, sum, ok := gatherHistogram(t, "external_request_duration_seconds", map[string]string{
		"dependency": "observe_dep", "method": "GET", "status": "200",
	})
	if !ok {
		t.Fatal("no series recorded for the observed labels")
	}
	if count != 2 {
		t.Fatalf("sample count = %d, want 2", count)
	}
	if math.Abs(sum-1.0) > 1e-9 {
		t.Fatalf("sample sum = %v, want 1", sum)
	}
}

func TestInstrumentedTransport_RecordsStatus(t *testing.T) {
	stub := &stubTransport{resp: &http.Response{StatusCode: http.StatusBadGateway, Body: http.NoBody}}
	rt := instrumentedTransport{dependency: "status_dep", base: stub}

	resp, err := rt.RoundTrip(httptest.NewRequest(http.MethodPost, "http://upstream/x", nil))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}

	count, _, ok := gatherHistogram(t, "external_request_duration_seconds", map[string]string{
		"dependency": "status_dep", "method": "POST", "status": "502",
	})
	if !ok || count != 1 {
		t.Fatalf("count = %d (found=%v), want 1 sample labelled with the response status", count, ok)
	}
}

func TestInstrumentedTransport_RecordsFailureAsError(t *testing.T) {
	wantErr := errors.New("dial tcp: connection refused")
	rt := instrumentedTransport{dependency: "error_dep", base: &stubTransport{err: wantErr}}

	_, err := rt.RoundTrip(httptest.NewRequest(http.MethodGet, "http://upstream/x", nil))
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want the transport error to propagate", err)
	}

	count, _, ok := gatherHistogram(t, "external_request_duration_seconds", map[string]string{
		"dependency": "error_dep", "method": "GET", "status": "error",
	})
	if !ok || count != 1 {
		t.Fatalf("count = %d (found=%v), want a failed round-trip recorded with status=error", count, ok)
	}
}

func TestInstrumentedTransport_DefaultsToDefaultTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	rt := instrumentedTransport{dependency: "default_dep"}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip with a nil base: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("status = %d, want 418", resp.StatusCode)
	}
	count, _, ok := gatherHistogram(t, "external_request_duration_seconds", map[string]string{
		"dependency": "default_dep", "method": "GET", "status": "418",
	})
	if !ok || count != 1 {
		t.Fatalf("count = %d (found=%v), want 1", count, ok)
	}
}

func TestNewInstrumentedClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewInstrumentedClient("client_dep", 3*time.Second)
	if client.Timeout != 3*time.Second {
		t.Fatalf("timeout = %v, want 3s", client.Timeout)
	}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()

	count, _, ok := gatherHistogram(t, "external_request_duration_seconds", map[string]string{
		"dependency": "client_dep", "method": "GET", "status": "200",
	})
	if !ok || count != 1 {
		t.Fatalf("count = %d (found=%v), want the client's request to be instrumented", count, ok)
	}
}
