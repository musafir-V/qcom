package middleware

import (
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/metrics"
)

// MetricsMiddleware records request latency and outcome as Prometheus metrics.
// It labels by the matched mux route *template* rather than the raw URL path to
// keep label cardinality bounded (a path like /api/v1/drivers/0971234567 is
// recorded as /api/v1/drivers/{phone}). Requests that match no route are
// bucketed under "unmatched".
//
// /health is skipped so ALB health-check traffic does not dominate the
// dashboards. OPTIONS preflight requests never reach here because CORSMiddleware
// short-circuits them earlier in the chain.
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := routeTemplate(r)
		if route == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(wrapped, r)

		metrics.ObserveRequest(r.Method, route, wrapped.statusCode, time.Since(start).Seconds())
	})
}

// routeTemplate returns the gorilla/mux path template for the matched route, or
// "unmatched" when no route matched (e.g. 404s) — which prevents unbounded,
// attacker-controlled paths from exploding the label space.
func routeTemplate(r *http.Request) string {
	if cur := mux.CurrentRoute(r); cur != nil {
		if tmpl, err := cur.GetPathTemplate(); err == nil {
			return tmpl
		}
	}
	return "unmatched"
}
