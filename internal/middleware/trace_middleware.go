package middleware

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/logging"
)

const (
	traceHeader   = "X-Trace-Id"
	requestHeader = "X-Request-Id"
)

// TraceIDMiddleware ensures every request has a stable trace ID for log
// correlation. It honors X-Trace-Id, then X-Request-Id, then generates a UUID.
// The chosen ID is stored on the request context and echoed back on the
// response so the client can quote it in support tickets.
func TraceIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(traceHeader)
		if id == "" {
			id = r.Header.Get(requestHeader)
		}
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set(traceHeader, id)
		ctx := logging.WithTraceID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
