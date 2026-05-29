package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

func newCapturingLogger() (*logrus.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	l := logrus.New()
	l.SetFormatter(&logrus.JSONFormatter{})
	l.SetOutput(buf)
	l.SetLevel(logrus.DebugLevel)
	return l, buf
}

func decodeOne(t *testing.T, buf *bytes.Buffer) map[string]interface{} {
	t.Helper()
	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("expected at least one log line, got none")
	}
	if strings.Count(line, "\n") != 0 {
		t.Fatalf("expected exactly one log line, got %d:\n%s", strings.Count(line, "\n")+1, line)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("invalid json log line: %v\nline=%q", err, line)
	}
	return m
}

func TestOp_Success_EmitsSingleInfoLine(t *testing.T) {
	logger, buf := newCapturingLogger()
	op := logging.Start(context.Background(), logger, "DoThing", logrus.Fields{"user_id": "u1"})
	op.End()

	m := decodeOne(t, buf)
	if m["level"] != "info" {
		t.Fatalf("expected info level, got %v", m["level"])
	}
	if m["msg"] != "op done" {
		t.Fatalf("expected msg=op done, got %v", m["msg"])
	}
	if m["op"] != "DoThing" {
		t.Fatalf("expected op=DoThing, got %v", m["op"])
	}
	if m["user_id"] != "u1" {
		t.Fatalf("expected user_id=u1, got %v", m["user_id"])
	}
	if _, ok := m["duration_ms"]; !ok {
		t.Fatal("expected duration_ms field on done line")
	}
	if _, ok := m["error"]; ok {
		t.Fatal("did not expect error field on a success line")
	}
}

func TestOp_Fail_EmitsErrorWithAttachedError(t *testing.T) {
	logger, buf := newCapturingLogger()
	wantErr := errors.New("boom")

	op := logging.Start(context.Background(), logger, "DoThing", nil)
	got := op.Fail(wantErr)
	op.End()

	if got != wantErr {
		t.Fatalf("Fail should return the err unchanged; got %v", got)
	}
	m := decodeOne(t, buf)
	if m["level"] != "error" {
		t.Fatalf("expected error level, got %v", m["level"])
	}
	if m["msg"] != "op failed" {
		t.Fatalf("expected msg=op failed, got %v", m["msg"])
	}
	if m["error"] != "boom" {
		t.Fatalf("expected attached error 'boom', got %v", m["error"])
	}
}

func TestOp_Outcome_StaysInfoAndCarriesOutcomeField(t *testing.T) {
	logger, buf := newCapturingLogger()
	sentinel := errors.New("forbidden")

	op := logging.Start(context.Background(), logger, "GetThing", nil)
	got := op.Outcome("forbidden", sentinel)
	op.End()

	if got != sentinel {
		t.Fatalf("Outcome should return the err unchanged; got %v", got)
	}
	m := decodeOne(t, buf)
	if m["level"] != "info" {
		t.Fatalf("Outcome must stay at Info level, got %v", m["level"])
	}
	if m["outcome"] != "forbidden" {
		t.Fatalf("expected outcome=forbidden, got %v", m["outcome"])
	}
	if _, ok := m["error"]; ok {
		t.Fatal("Outcome must NOT attach the err to the log line")
	}
}

func TestOp_With_AccumulatesFields(t *testing.T) {
	logger, buf := newCapturingLogger()

	op := logging.Start(context.Background(), logger, "List", logrus.Fields{"user_id": "u1"})
	op.With("count", 42).With("cache_hit", true)
	op.End()

	m := decodeOne(t, buf)
	if m["count"].(float64) != 42 {
		t.Fatalf("expected count=42, got %v", m["count"])
	}
	if m["cache_hit"] != true {
		t.Fatalf("expected cache_hit=true, got %v", m["cache_hit"])
	}
	if m["user_id"] != "u1" {
		t.Fatalf("expected starting field to survive, got %v", m["user_id"])
	}
}

func TestOp_End_IsIdempotent(t *testing.T) {
	logger, buf := newCapturingLogger()

	op := logging.Start(context.Background(), logger, "Once", nil)
	op.End()
	op.End()
	op.End()

	lines := strings.Count(strings.TrimSpace(buf.String()), "\n")
	if lines != 0 {
		t.Fatalf("expected exactly 1 line, got %d", lines+1)
	}
}

func TestOp_FailNil_DoesNotPromoteToError(t *testing.T) {
	logger, buf := newCapturingLogger()

	op := logging.Start(context.Background(), logger, "DoThing", nil)
	op.Fail(nil)
	op.End()

	m := decodeOne(t, buf)
	if m["level"] != "info" {
		t.Fatalf("Fail(nil) must not promote to error; got %v", m["level"])
	}
}

func TestOp_TraceIDFromContextIsAttached(t *testing.T) {
	logger, buf := newCapturingLogger()
	ctx := logging.WithTraceID(context.Background(), "tid-99")

	op := logging.Start(ctx, logger, "DoThing", nil)
	op.End()

	m := decodeOne(t, buf)
	if m["trace_id"] != "tid-99" {
		t.Fatalf("expected trace_id=tid-99, got %v", m["trace_id"])
	}
}

func TestOp_NilSafe(t *testing.T) {
	var op *logging.Op
	// All methods on a nil *Op must be no-ops; this guards against panics if a
	// caller misuses the helper.
	_ = op.With("k", "v")
	_ = op.Fail(errors.New("x"))
	_ = op.Outcome("o", errors.New("x"))
	op.End() // must not panic
}
