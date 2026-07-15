package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// externalRequestDuration tracks the latency and outcome of every outgoing HTTP
// request qcom makes to an external dependency. The "dependency" label is a
// stable, low-cardinality name for the upstream (e.g. "java_order_service"),
// "method" is the HTTP verb, and "status" is the numeric HTTP status code —
// or "error" when the request never produced a response (timeout, DNS,
// connection refused, context cancellation). Latency, error rate, and success
// rate are all derived from this one histogram.
var externalRequestDuration = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name: "external_request_duration_seconds",
		Help: "Duration of outgoing HTTP requests from qcom to external dependencies, by dependency, method, and status.",
		// External hops (Google, Vonage, the Java order-service) are slower and
		// more variable than in-process handlers, so this ladder runs out to 10s.
		Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 0.75, 1, 2, 5, 10},
	},
	[]string{"dependency", "method", "status"},
)

func init() {
	registry.MustRegister(externalRequestDuration)
}

// ObserveExternalRequest records a single outgoing request against a dependency.
func ObserveExternalRequest(dependency, method, status string, seconds float64) {
	externalRequestDuration.WithLabelValues(dependency, method, status).Observe(seconds)
}

// instrumentedTransport is an http.RoundTripper that times the wrapped call and
// records it under a fixed dependency label. Failed round-trips (no response)
// are recorded with status "error" so they still count against the error rate.
type instrumentedTransport struct {
	dependency string
	base       http.RoundTripper
}

func (t instrumentedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	start := time.Now()
	resp, err := base.RoundTrip(req)
	status := "error"
	if err == nil && resp != nil {
		status = strconv.Itoa(resp.StatusCode)
	}
	ObserveExternalRequest(t.dependency, req.Method, status, time.Since(start).Seconds())
	return resp, err
}

// NewInstrumentedClient returns an *http.Client with the given timeout whose
// transport records external_request_duration_seconds under dependency. It is a
// drop-in replacement for &http.Client{Timeout: timeout}.
func NewInstrumentedClient(dependency string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: instrumentedTransport{dependency: dependency, base: http.DefaultTransport},
	}
}
