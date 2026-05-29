package logging

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"
)

// Op tracks a single named operation. Construct with Start, enrich with With,
// terminate one of three ways:
//
//	op := logging.Start(ctx, logger, "GetByID", logrus.Fields{"address_id": id})
//	defer op.End()
//	...
//	if err != nil { return nil, op.Fail(err) }       // Error level, WithError attached
//	if !ok       { return nil, op.Outcome("forbidden", ErrForbidden) } // Info level
//	op.With("count", n)                              // mid-method enrichment
//
// End emits exactly one structured line at the chosen level. duration_ms and
// the op name are added automatically.
type Op struct {
	entry  *logrus.Entry
	name   string
	start  time.Time
	fields logrus.Fields
	err    error
	level  logrus.Level
	done   bool
}

// Start captures the start time and starting fields for an operation.
// The returned Op MUST be finalized with End (typically via defer).
func Start(ctx context.Context, base *logrus.Logger, name string, fields logrus.Fields) *Op {
	f := make(logrus.Fields, len(fields)+2)
	for k, v := range fields {
		f[k] = v
	}
	return &Op{
		entry:  FromContext(ctx, base),
		name:   name,
		start:  time.Now(),
		fields: f,
		level:  logrus.InfoLevel,
	}
}

// With attaches a field to be emitted at End. Returns the receiver for chaining.
func (o *Op) With(key string, value interface{}) *Op {
	if o == nil {
		return nil
	}
	o.fields[key] = value
	return o
}

// Fail marks the operation as failed with an infrastructure error.
// End will emit at Error level with WithError attached. Returns err for terse
// return sites: `return nil, op.Fail(err)`. Passing nil is a no-op.
func (o *Op) Fail(err error) error {
	if o == nil || err == nil {
		return err
	}
	o.err = err
	o.level = logrus.ErrorLevel
	return err
}

// Outcome records a business outcome (e.g. "not_found", "forbidden", "expired").
// End stays at Info level; the outcome is added as a field. The returned err is
// passed through unchanged for terse return sites:
// `return nil, op.Outcome("forbidden", ErrForbidden)`.
func (o *Op) Outcome(outcome string, err error) error {
	if o == nil {
		return err
	}
	o.fields["outcome"] = outcome
	return err
}

// Logger returns a logrus entry derived from the op's starting fields. Use it
// for mid-operation observations (typically Warn) that should share the trace
// id but are not part of the op's termination line.
func (o *Op) Logger() *logrus.Entry {
	if o == nil {
		return logrus.NewEntry(logrus.StandardLogger())
	}
	return o.entry
}

// End emits the single structured line for this op. Safe to call twice;
// only the first call writes.
func (o *Op) End() {
	if o == nil || o.done {
		return
	}
	o.done = true
	o.fields["op"] = o.name
	o.fields["duration_ms"] = time.Since(o.start).Milliseconds()
	entry := o.entry.WithFields(o.fields)
	if o.err != nil {
		entry = entry.WithError(o.err)
	}
	switch o.level {
	case logrus.ErrorLevel:
		entry.Error("op failed")
	default:
		entry.Info("op done")
	}
}
