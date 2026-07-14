// Package metrics holds qcom's Prometheus instrumentation. It owns a private
// registry (so we control exactly which series are exported) and exposes a
// single request-latency histogram plus a handler for the scrape endpoint.
//
// The histogram is the one source of truth for the API dashboards: request
// rate comes from its _count, error/success rate from filtering on the status
// label, and latency percentiles from histogram_quantile over its buckets.
package metrics

import (
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// registry is dedicated to qcom rather than the global default, so nothing
// pulls in stray series we did not opt into.
var registry = prometheus.NewRegistry()

// requestDuration is labelled by HTTP method, the matched mux route *template*
// (e.g. /api/v1/drivers/{phone}, never the raw path), and the numeric status
// code. Buckets are dense below 2s and capped at 2s: anything slower lands in
// the implicit +Inf bucket, which is acceptable since these APIs target well
// under 2s.
var requestDuration = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "Duration of qcom HTTP requests by method, route template, and status code.",
		Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.15, 0.2, 0.3, 0.5, 0.75, 1, 1.5, 2},
	},
	[]string{"method", "route", "status"},
)

func init() {
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		requestDuration,
	)
}

// ObserveRequest records one completed HTTP request.
func ObserveRequest(method, route string, status int, seconds float64) {
	requestDuration.WithLabelValues(method, route, strconv.Itoa(status)).Observe(seconds)
}

// Handler serves the Prometheus exposition format for qcom's registry. It is
// mounted on the internal, localhost-only metrics server (never the public
// API router), so no internal telemetry is exposed at the edge.
func Handler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}
