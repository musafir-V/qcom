package logging_test

import (
	"context"
	"testing"

	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

func TestTraceIDFromContext_EmptyWhenAbsent(t *testing.T) {
	if got := logging.TraceIDFromContext(context.Background()); got != "" {
		t.Fatalf("expected empty trace id, got %q", got)
	}
}

func TestTraceIDFromContext_RoundTrip(t *testing.T) {
	ctx := logging.WithTraceID(context.Background(), "abc-123")
	if got := logging.TraceIDFromContext(ctx); got != "abc-123" {
		t.Fatalf("expected abc-123, got %q", got)
	}
}

func TestFromContext_NoTraceID(t *testing.T) {
	base := logrus.New()
	entry := logging.FromContext(context.Background(), base)
	if entry == nil {
		t.Fatal("expected a non-nil entry")
	}
	if _, ok := entry.Data["trace_id"]; ok {
		t.Fatal("expected no trace_id field when ctx has none")
	}
}

func TestFromContext_WithTraceID(t *testing.T) {
	base := logrus.New()
	ctx := logging.WithTraceID(context.Background(), "tid-xyz")
	entry := logging.FromContext(ctx, base)
	got, ok := entry.Data["trace_id"]
	if !ok {
		t.Fatal("expected trace_id field on entry")
	}
	if got != "tid-xyz" {
		t.Fatalf("expected tid-xyz, got %v", got)
	}
}

func TestFromContext_NilCtx(t *testing.T) {
	base := logrus.New()
	entry := logging.FromContext(nil, base)
	if entry == nil {
		t.Fatal("expected non-nil entry for nil ctx")
	}
}
