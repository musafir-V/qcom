package logging

import (
	"context"

	"github.com/sirupsen/logrus"
)

type ctxKey int

const traceIDKey ctxKey = 1

// WithTraceID returns a copy of ctx that carries the given trace ID.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// TraceIDFromContext returns the trace ID stored in ctx, or "" if absent.
func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(traceIDKey).(string); ok {
		return v
	}
	return ""
}

// FromContext returns a *logrus.Entry derived from base. If ctx carries a
// trace ID, it is attached as the "trace_id" field. Safe to call with a nil
// ctx (returns a plain entry built from base).
func FromContext(ctx context.Context, base *logrus.Logger) *logrus.Entry {
	if ctx == nil {
		return logrus.NewEntry(base)
	}
	id := TraceIDFromContext(ctx)
	if id == "" {
		return logrus.NewEntry(base)
	}
	return base.WithField("trace_id", id)
}
