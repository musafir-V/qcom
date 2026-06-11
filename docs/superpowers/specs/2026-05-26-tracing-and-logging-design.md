# Trace ID + DB/External Call Logging — Design

## Problem

Today, repositories and external-service callers log only on error and only
through the raw `*logrus.Logger` passed at construction. There is no per-request
correlation — when something goes wrong in production, you cannot pull all the
log lines for a single user request out of the stream. There is also no
visibility into successful DB queries or successful external HTTP calls (Google
Geocoder, Google Distance Matrix for ETA, S3), which makes latency and
behavioural debugging guesswork.

## Goal

1. Inject a stable `trace_id` into every log line produced while handling a
   single HTTP request, so logs for one request can be filtered in aggregate
   storage with a single query.
2. Add `start` + `finish` (+ `duration_ms`) log lines at every DB call site,
   every external HTTP call site, and every top-level service entry point. Keep
   existing error logs but route them through the same contextual logger.

## Non-Goals

- Distributed tracing systems (Jaeger, OpenTelemetry, etc.). We are adding a
  trace ID that flows through logs only.
- Restructuring constructors to swap `*logrus.Logger` for a custom interface.
  We keep the existing dependency-injection pattern.
- Redis instrumentation — Redis client is not currently invoked from any
  request-path code in this repo.
- Instrumenting pure-compute helpers (polygon math, JWT signing, etc.) — they
  do not perform I/O.
- New unit tests for repositories. We will only add minimal tests for the new
  `internal/logging` helpers.

## Approach

### Trace-ID flow

1. A new `TraceIDMiddleware` runs as the OUTERMOST middleware on every HTTP
   request.
2. It reads `X-Trace-Id` from the request; if absent it falls back to
   `X-Request-Id`; if both are absent it generates a UUID v4 via
   `github.com/google/uuid` (already in `go.mod`).
3. It stores the ID in `r.Context()` via `logging.WithTraceID(ctx, id)` and
   writes `X-Trace-Id: <id>` on the response so the client can quote it in
   support tickets.
4. The existing `LoggingMiddleware` runs INSIDE `TraceIDMiddleware`. Its
   request log line is updated to use the contextual logger so the `trace_id`
   appears on the HTTP request log.
5. Every downstream method that performs I/O fetches a contextual logger via
   `logging.FromContext(ctx, r.logger)` and uses that for all log lines in the
   method. The trace ID is attached automatically.

### Logging package (new, small)

`internal/logging/logging.go`:

```go
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
    if v, ok := ctx.Value(traceIDKey).(string); ok {
        return v
    }
    return ""
}

// FromContext returns a logrus.Entry derived from base, with the trace_id
// field attached if one is present in ctx. Safe to call with a nil ctx
// (e.g. from startup code) — it returns base.WithFields(nil).
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
```

Tests for this package: a single `logging_test.go` that exercises
`WithTraceID` → `TraceIDFromContext`, `FromContext` with and without a trace
ID, and the nil-ctx path. ~30 lines.

### Trace middleware (new)

`internal/middleware/trace_middleware.go`:

```go
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
```

Wired into the router in `cmd/server/main.go` BEFORE the existing logging
middleware:

```go
router.Use(middleware.CORSMiddleware)
router.Use(middleware.TraceIDMiddleware)   // new
router.Use(middleware.LoggingMiddleware(logger))
```

### Updated LoggingMiddleware

The existing middleware constructs `logger.WithFields(...).Info("HTTP request")`
directly on the base logger. Change it to:

```go
logging.FromContext(r.Context(), logger).WithFields(logrus.Fields{ ... }).Info("HTTP request")
```

so the per-request log line carries `trace_id`.

### Per-call logging convention

At each DB-call, external-call, and top-level service method, follow this
template:

```go
log := logging.FromContext(ctx, r.logger)
start := time.Now()

log.WithFields(logrus.Fields{
    "op":         "<short verb, e.g. GetByID / Query / ReverseGeocode>",
    // identifying parameters: address_id, user_id, table, lat, lng, etc.
}).Info("<layer> call start")

// existing work...

if err != nil {
    log.WithError(err).WithFields(logrus.Fields{
        "op":          "<same op>",
        "duration_ms": time.Since(start).Milliseconds(),
    }).Error("<layer> call failed")
    return ..., err
}

log.WithFields(logrus.Fields{
    "op":          "<same op>",
    "duration_ms": time.Since(start).Milliseconds(),
    // optional: result_count, status_code
}).Info("<layer> call done")
```

Layer label values: `"dynamodb"`, `"google_geocode"`, `"google_distance_matrix"`,
`"s3"`, `"service"`.

### Sites to instrument

**Repositories (all under `internal/repository/`):**

- `address_repository.go` — `Create`, `GetByID`, `QueryByUserID`, `Update`, `Delete` (and any other exported method present in the file).
- `darkstore_repository.go` — `ListActive` and any other methods.
- `de_repository.go` — every exported method.
- `eta_repository.go` — every exported method on the repository.
- `otp_repository.go` — every exported method.
- `page_repository.go` — every exported method.
- `refresh_token_repository.go` — every exported method.
- `user_repository.go` — every exported method.

For each method, log the operation name and the identifying input params
(IDs, table name); on done, include `duration_ms` and (where natural) a
`count` for queries that return slices.

**External services (under `internal/service/`):**

- `geocoding_service.go` — around the `g.httpClient.Do(req)` call in
  `ReverseGeocode`. Layer `google_geocode`, fields `lat`, `lng`,
  `status_code` on done.
- `eta_service.go` — around the Google Distance Matrix HTTP call. Layer
  `google_distance_matrix`, fields `origin`, `destination`, `status_code`.
  Existing cache hit/miss path stays in-place; only the external HTTP segment
  gets new start/done lines.
- `upload_service.go` — around the S3 PutObject / Presign calls. Layer `s3`,
  fields `bucket`, `key`, `op` (`presign` / `put_object`).

**Top-level service entry points (under `internal/service/`):**

Add start/done logs at the public method boundary for the high-level service
operations actually invoked by handlers. Specifically:

- `address_service.go` — every exported method called from handlers.
- `serviceability_service.go` — `CheckServiceability`.
- `de_service.go` — every exported method called from handlers.
- `otp_service.go` — every exported method called from handlers.
- `jwt_service.go` — only methods invoked from a request handler. Skip
  pure-compute helpers (signing/verifying done inside the service itself
  without I/O).
- `upload_service.go` — every exported method (these also wrap external calls;
  the service-level log is the wrapper, the external-call log is the inner
  segment).
- `refresh_token_service.go` — every exported method called from handlers.

The implementer should enumerate the exact methods from each file when writing
the plan; the rule is: instrument anything called by a handler.

## Error Handling

- All existing error-return behaviour is preserved. We add log lines, we never
  change control flow or wrap errors differently.
- `log.WithError(err).Error(...)` replaces any existing `r.logger.WithError(err)`
  call so error logs carry `trace_id` too.

## Configuration

No new env vars. No new config struct fields. The middleware always runs.

## Migration / Rollout

Single PR. The change is additive: code that ignores `ctx`-derived loggers
continues to work (the `*logrus.Logger` passed at construction is the fallback
for `FromContext`). Existing log consumers see new `trace_id` and new
start/done lines; the existing log fields are unchanged.

## Risk

- **Log volume increase.** Roughly doubles for the request path. Acceptable
  for development; if production cost is a concern after rollout, downgrade
  start-line level to Debug behind an env var (out of scope for this PR).
- **Performance.** `logging.FromContext` is two map lookups and a logrus entry
  allocation per logged line — negligible compared to the I/O it surrounds.
- **Context plumbing oversights.** Any code path that does `context.Background()`
  internally loses the trace ID. The plan should grep for `context.Background`
  and `context.TODO` in the request path and flag any that should be
  propagating ctx instead.

## Files Touched

**New:**
- `internal/logging/logging.go`
- `internal/logging/logging_test.go`
- `internal/middleware/trace_middleware.go`

**Modified:**
- `internal/middleware/logging_middleware.go`
- `cmd/server/main.go` (router wiring)
- All 8 files under `internal/repository/`
- `internal/service/geocoding_service.go`
- `internal/service/eta_service.go`
- `internal/service/upload_service.go`
- `internal/service/address_service.go`
- `internal/service/serviceability_service.go`
- `internal/service/de_service.go`
- `internal/service/otp_service.go`
- `internal/service/jwt_service.go` (handler-invoked methods only)
- `internal/service/refresh_token_service.go`

## Phased Implementation

The plan that follows will split the work into four phases that build on each
other:

1. **Logging helper** — `internal/logging` package + tests.
2. **Trace middleware + wired into router** — including the LoggingMiddleware
   update. After this phase, every request log carries `trace_id`.
3. **Repository instrumentation** — start/done/error logs in all 8 repos.
4. **Service instrumentation** — external HTTP calls AND top-level service
   entry points. Sub-divided per file inside the plan task to keep diffs
   reviewable.

Each phase produces a green build and (where applicable) green tests.
