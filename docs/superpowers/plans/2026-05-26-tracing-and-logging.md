# Trace ID + DB/External Call Logging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Inject a per-request `trace_id` into the request context, propagate it through every log line in the request path, and add `start` / `done` / `duration_ms` logs around every DB call, external HTTP call, and top-level service entry point.

**Architecture:** A new `internal/logging` package holds context helpers (`WithTraceID`, `TraceIDFromContext`, `FromContext`). A new `TraceIDMiddleware` runs outermost and writes the trace ID into both `r.Context()` and the `X-Trace-Id` response header. Every I/O-performing method derives a contextual `*logrus.Entry` via `logging.FromContext(ctx, r.logger)` so log lines auto-attach the trace ID. No constructor signatures change for repos or services — except `OTPService.GenerateOTP` / `VerifyOTP`, which must take a `ctx context.Context` to propagate the trace ID (currently they call `context.Background()`).

**Tech Stack:** Go, logrus, gorilla/mux, `github.com/google/uuid` (already in `go.mod`).

**Spec:** [docs/superpowers/specs/2026-05-26-tracing-and-logging-design.md](../specs/2026-05-26-tracing-and-logging-design.md)

---

## Conventions Used By All Tasks

**Branch:** all commits go on a single feature branch created at Task 1 Step 1.

**Logging pattern.** At the top of every instrumented method:

```go
log := logging.FromContext(ctx, r.logger) // or s.logger / g.logger
start := time.Now()

log.WithFields(logrus.Fields{
    "op":      "<verb>",
    // identifying params
}).Info("<layer> call start")

// ... existing work ...

if err != nil {
    log.WithError(err).WithFields(logrus.Fields{
        "op":          "<verb>",
        "duration_ms": time.Since(start).Milliseconds(),
    }).Error("<layer> call failed")
    return ..., err
}

log.WithFields(logrus.Fields{
    "op":          "<verb>",
    "duration_ms": time.Since(start).Milliseconds(),
    // optional: result count, status code, cache hit/miss
}).Info("<layer> call done")
```

**Layer values:** `"dynamodb"`, `"google_geocode"`, `"google_distance_matrix"`, `"s3"`, `"service"`.

**Existing error logs** (e.g. `r.logger.WithError(err).Error("Failed to ...")`) are REPLACED by the new error log line in the template — do not leave both. The new line uses `log` (the contextual entry), keeping all existing fields and adding `op` + `duration_ms`.

---

### Task 1: Logging helper package + branch

**Files:**
- Create: `internal/logging/logging.go`
- Create: `internal/logging/logging_test.go`

- [ ] **Step 1: Create branch**

```bash
cd /Users/shivangawasthi/bunzo/qcom
git checkout main
git pull --ff-only origin main
git checkout -b feat/trace-id-logging
```

- [ ] **Step 2: Write the failing test**

Create `internal/logging/logging_test.go` with:

```go
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
```

- [ ] **Step 3: Verify the test fails (package missing)**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go test ./internal/logging/...`
Expected: FAIL with `no Go files in ... internal/logging` or `package not found`.

- [ ] **Step 4: Create the logging package**

Create `internal/logging/logging.go`:

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
```

- [ ] **Step 5: Verify the test passes**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go test ./internal/logging/... -v`
Expected: all five test cases PASS.

- [ ] **Step 6: Build the module**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go build ./...`
Expected: exits 0, no output.

- [ ] **Step 7: Commit**

```bash
cd /Users/shivangawasthi/bunzo/qcom
git add internal/logging/logging.go internal/logging/logging_test.go
git commit -m "logging: add trace-id context helpers"
```

---

### Task 2: TraceID middleware + LoggingMiddleware update + router wiring

**Files:**
- Create: `internal/middleware/trace_middleware.go`
- Modify: `internal/middleware/logging_middleware.go`
- Modify: `cmd/server/main.go` (the `setupRouter` function, after `router.Use(middleware.CORSMiddleware)`)

- [ ] **Step 1: Create TraceIDMiddleware**

Create `internal/middleware/trace_middleware.go`:

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
```

- [ ] **Step 2: Update LoggingMiddleware to use the contextual logger**

In `internal/middleware/logging_middleware.go`, change the file to:

```go
package middleware

import (
	"net/http"
	"time"

	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

func LoggingMiddleware(logger *logrus.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(wrapped, r)

			duration := time.Since(start)

			logging.FromContext(r.Context(), logger).WithFields(logrus.Fields{
				"method":      r.Method,
				"path":        r.URL.Path,
				"status":      wrapped.statusCode,
				"duration":    duration,
				"remote_addr": r.RemoteAddr,
			}).Info("HTTP request")
		})
	}
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}
```

- [ ] **Step 3: Wire TraceIDMiddleware into the router**

In `cmd/server/main.go`, locate the `setupRouter` function (around line 185). The current middleware block reads:

```go
	router.Use(middleware.CORSMiddleware)
	router.Use(middleware.LoggingMiddleware(logger))
```

Change it to:

```go
	router.Use(middleware.CORSMiddleware)
	router.Use(middleware.TraceIDMiddleware)
	router.Use(middleware.LoggingMiddleware(logger))
```

Order matters: TraceIDMiddleware MUST run BEFORE LoggingMiddleware so the request log line carries `trace_id`.

- [ ] **Step 4: Build the module**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go build ./...`
Expected: exits 0, no output.

- [ ] **Step 5: Vet the module**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go vet ./...`
Expected: exits 0, no output.

- [ ] **Step 6: Run tests**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go test ./...`
Expected: all tests pass (no new failures vs main).

- [ ] **Step 7: Smoke test — confirm trace ID round-trips**

```bash
cd /Users/shivangawasthi/bunzo/qcom
JWT_SECRET_KEY=test-secret-keys-are-at-least-32-bytes-long go run ./cmd/server &
SERVER_PID=$!
sleep 2

# 1. Request without trace header — server should generate one
echo "--- generated ---"
curl -sS -D - http://localhost:8080/health -o /dev/null | grep -i 'x-trace-id'

# 2. Request with a supplied trace header — server should echo it back
echo "--- supplied ---"
curl -sS -D - -H 'X-Trace-Id: my-test-id-001' http://localhost:8080/health -o /dev/null | grep -i 'x-trace-id'

kill $SERVER_PID
```

Expected: first call returns a UUID-shaped `X-Trace-Id`; second call returns exactly `X-Trace-Id: my-test-id-001`.

If `JWT_SECRET_KEY` causes `Load()` to fail for another reason, set whatever extra env vars are required to boot — the test is only about the middleware.

- [ ] **Step 8: Commit**

```bash
cd /Users/shivangawasthi/bunzo/qcom
git add internal/middleware/trace_middleware.go internal/middleware/logging_middleware.go cmd/server/main.go
git commit -m "middleware: add trace-id middleware and propagate into request logs"
```

---

### Task 3: Instrument all repositories

**Files (modify each):**
- `internal/repository/address_repository.go` — methods: `Create`, `GetByID`, `QueryByUserID`, `SoftDelete`, `UpdateReceiverDetails`
- `internal/repository/darkstore_repository.go` — methods: `ListActive`
- `internal/repository/de_repository.go` — methods: `Create`, `Exists`, `FindEligibleByStore`, `GetByPhone`, `UpdateStatus`
- `internal/repository/eta_repository.go` — methods: `Get`, `Save`
- `internal/repository/otp_repository.go` — methods: `Delete`, `Get`, `Store`, `StoreTestOTP`
- `internal/repository/page_repository.go` — methods: `GetPageByKey`
- `internal/repository/refresh_token_repository.go` — methods: `Delete`, `Get`, `GetByFamilyID`, `IsRevoked`, `MarkRevoked`, `Store`
- `internal/repository/user_repository.go` — methods: `Create`, `GetByPhoneNumber`, `GetOrCreate`, `Update`

For EVERY listed method:

1. Add at the top of the function body (right after the opening brace, before any other code):
   ```go
   log := logging.FromContext(ctx, r.logger)
   start := time.Now()
   log.WithFields(logrus.Fields{
       "op":   "<method name verbatim>",
       // identifying inputs as fields (see per-method guidance below)
   }).Info("dynamodb call start")
   ```

2. Replace each `r.logger.WithError(err).Error("...")` (or `Warn`, `Info`) with a call using `log` instead of `r.logger`, AND attach `op` + `duration_ms`. Example:

   Before:
   ```go
   r.logger.WithError(err).Error("Failed to create address in DynamoDB")
   return fmt.Errorf("failed to create address: %w", err)
   ```

   After:
   ```go
   log.WithError(err).WithFields(logrus.Fields{
       "op":          "Create",
       "duration_ms": time.Since(start).Milliseconds(),
   }).Error("dynamodb call failed")
   return fmt.Errorf("failed to create address: %w", err)
   ```

3. Immediately before each successful `return` (i.e. on every happy-path exit), insert:
   ```go
   log.WithFields(logrus.Fields{
       "op":          "<method name>",
       "duration_ms": time.Since(start).Milliseconds(),
       // optional: count, found
   }).Info("dynamodb call done")
   ```

4. Add the `time` import and the `github.com/qcom/qcom/internal/logging` import to every modified file (only if not already present).

**Per-method identifying fields to include in the start log line:**

| File | Method | start fields |
| --- | --- | --- |
| address_repository.go | Create | `"address_id": address.AddressID` (or whatever ID field exists on the model; if unsure, omit and only log `op`) |
| address_repository.go | GetByID | `"address_id": addressID` |
| address_repository.go | QueryByUserID | `"user_id": userID` |
| address_repository.go | SoftDelete | `"address_id": addressID` |
| address_repository.go | UpdateReceiverDetails | `"address_id": addressID` |
| darkstore_repository.go | ListActive | (no extra fields) |
| de_repository.go | Create | `"phone": de.Phone` (if available; otherwise omit) |
| de_repository.go | Exists | `"phone": phone` |
| de_repository.go | FindEligibleByStore | `"store_id": storeID` |
| de_repository.go | GetByPhone | `"phone": phone` |
| de_repository.go | UpdateStatus | `"phone": phone, "status": string(status)` |
| eta_repository.go | Get | `"h3_cell": h3Cell` |
| eta_repository.go | Save | `"h3_cell": h3Cell` |
| otp_repository.go | Delete | `"phone": phoneNumber` |
| otp_repository.go | Get | `"phone": phoneNumber` |
| otp_repository.go | Store | `"phone": phoneNumber` |
| otp_repository.go | StoreTestOTP | `"phone": phoneNumber` |
| page_repository.go | GetPageByKey | `"pk": pk` |
| refresh_token_repository.go | Delete | `"jti": jti` |
| refresh_token_repository.go | Get | `"jti": jti` |
| refresh_token_repository.go | GetByFamilyID | `"family_id": familyID` |
| refresh_token_repository.go | IsRevoked | `"jti": jti` |
| refresh_token_repository.go | MarkRevoked | `"jti": jti` |
| refresh_token_repository.go | Store | `"jti": tokenData.JTI` (or whichever field on RefreshTokenData identifies the row — omit if unclear) |
| user_repository.go | Create | `"phone": user.PhoneNumber` (or skip if not available) |
| user_repository.go | GetByPhoneNumber | `"phone": phoneNumber` |
| user_repository.go | GetOrCreate | `"phone": phoneNumber` |
| user_repository.go | Update | `"phone": user.PhoneNumber` (or skip) |

For methods that return a slice (e.g. `QueryByUserID`, `FindEligibleByStore`, `GetByFamilyID`), include `"count": len(result)` on the done log line.

For methods that return `(*T, error)` where the row may not exist (`Get`, `GetByID`, `GetByPhone`, etc.), include `"found": result != nil` on the done log line.

- [ ] **Step 1: Apply the pattern to every file listed above**

Edit each repository file according to the guidance above. Work file by file. The mechanical structure of every method is:

```go
func (r *XRepository) MethodName(ctx context.Context, ...) (..., error) {
    log := logging.FromContext(ctx, r.logger)
    start := time.Now()
    log.WithFields(logrus.Fields{"op": "MethodName", /* identifying fields */}).Info("dynamodb call start")

    // ... existing body ...
    // - every error-return path: log with WithError + op + duration_ms, then `return`
    // - every success-return path: log "done" with op + duration_ms + optional count/found, then `return`
}
```

When in doubt about an identifying field, omit it (only `op` is required). NEVER log secrets — no OTP plaintext (it's already hashed before the repo sees it, but be careful with `Store`/`StoreTestOTP`: log only `phone`, never the value/hash). No tokens (`MarkRevoked` and friends log only `jti`, never the token string).

- [ ] **Step 2: Build the module**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go build ./...`
Expected: exits 0, no output.

- [ ] **Step 3: Vet the module**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go vet ./...`
Expected: exits 0, no output.

- [ ] **Step 4: Run tests**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go test ./...`
Expected: all tests pass. No tests should newly fail.

- [ ] **Step 5: Sanity check — every repository file imports logging**

Run:
```bash
cd /Users/shivangawasthi/bunzo/qcom
grep -L 'internal/logging' internal/repository/*.go
```
Expected: empty output (every repo file imports `internal/logging`).

- [ ] **Step 6: Sanity check — no orphan calls to `r.logger.WithError` remain**

Run:
```bash
cd /Users/shivangawasthi/bunzo/qcom
grep -n 'r\.logger\.WithError' internal/repository/*.go
```
Expected: empty output (the rewrite replaced all of these with `log.WithError`).

- [ ] **Step 7: Commit**

```bash
cd /Users/shivangawasthi/bunzo/qcom
git add internal/repository/
git commit -m "repository: log start/done/duration on every dynamodb call"
```

---

### Task 4: OTPService — propagate ctx + instrument

**Files:**
- Modify: `internal/service/otp_service.go`
- Modify: `internal/handlers/auth_handlers.go` (two call sites)

The current `OTPService.GenerateOTP` and `VerifyOTP` do NOT accept a `context.Context` and instead call `context.Background()` internally (see lines 54 and 74). This destroys the trace ID, so step one is to change the signatures and propagate from the handler.

- [ ] **Step 1: Change OTPService signatures and bodies**

In `internal/service/otp_service.go`, replace the file with the following content. Note: this includes the trace-id-aware logging too.

```go
package service

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

type OTPService struct {
	otpRepo *repository.OTPRepository
	cfg     *config.OTPConfig
	logger  *logrus.Logger
}

func NewOTPService(otpRepo *repository.OTPRepository, cfg *config.OTPConfig, logger *logrus.Logger) *OTPService {
	return &OTPService{
		otpRepo: otpRepo,
		cfg:     cfg,
		logger:  logger,
	}
}

func (s *OTPService) GenerateOTP(ctx context.Context, phoneNumber string) (string, error) {
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{"op": "GenerateOTP", "phone": phoneNumber}).Info("service call start")

	// TODO: uncomment random OTP generation before production
	// otp, err := s.generateRandomOTP(s.cfg.Length)
	// if err != nil {
	// 	return "", fmt.Errorf("failed to generate OTP: %w", err)
	// }
	otp := "112233"

	hashedOTP, err := bcrypt.GenerateFromPassword([]byte(otp), bcrypt.DefaultCost)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GenerateOTP",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return "", fmt.Errorf("failed to hash OTP: %w", err)
	}

	otpData := models.OTPData{
		OTPHash:   string(hashedOTP),
		Phone:     phoneNumber,
		Attempts:  0,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(s.cfg.Expiry),
	}

	if err := s.otpRepo.Store(ctx, phoneNumber, otpData); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GenerateOTP",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return "", err
	}

	if err := s.otpRepo.StoreTestOTP(ctx, phoneNumber, otp, otpData.ExpiresAt); err != nil {
		log.WithError(err).Warn("Failed to store test OTP")
	}

	log.WithFields(logrus.Fields{
		"phone": phoneNumber,
		"otp":   otp,
	}).Info("OTP generated (logged for development)")

	log.WithFields(logrus.Fields{
		"op":          "GenerateOTP",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("service call done")
	return otp, nil
}

func (s *OTPService) VerifyOTP(ctx context.Context, phoneNumber, otp string) (bool, error) {
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{"op": "VerifyOTP", "phone": phoneNumber}).Info("service call start")

	otpData, err := s.otpRepo.Get(ctx, phoneNumber)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "VerifyOTP",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return false, err
	}

	if time.Now().After(otpData.ExpiresAt) {
		s.otpRepo.Delete(ctx, phoneNumber)
		log.WithFields(logrus.Fields{
			"op":          "VerifyOTP",
			"duration_ms": time.Since(start).Milliseconds(),
			"outcome":     "expired",
		}).Info("service call done")
		return false, fmt.Errorf("OTP expired")
	}

	if otpData.Attempts >= s.cfg.MaxAttempts {
		s.otpRepo.Delete(ctx, phoneNumber)
		log.WithFields(logrus.Fields{
			"op":          "VerifyOTP",
			"duration_ms": time.Since(start).Milliseconds(),
			"outcome":     "max_attempts",
		}).Info("service call done")
		return false, fmt.Errorf("maximum attempts exceeded")
	}

	err = bcrypt.CompareHashAndPassword([]byte(otpData.OTPHash), []byte(otp))
	if err != nil {
		otpData.Attempts++
		s.otpRepo.Store(ctx, phoneNumber, *otpData)
		log.WithFields(logrus.Fields{
			"op":          "VerifyOTP",
			"duration_ms": time.Since(start).Milliseconds(),
			"outcome":     "invalid",
		}).Info("service call done")
		return false, fmt.Errorf("invalid OTP")
	}

	s.otpRepo.Delete(ctx, phoneNumber)
	log.WithFields(logrus.Fields{
		"op":          "VerifyOTP",
		"duration_ms": time.Since(start).Milliseconds(),
		"outcome":     "verified",
	}).Info("service call done")
	return true, nil
}

func (s *OTPService) generateRandomOTP(length int) (string, error) {
	otp := ""
	for i := 0; i < length; i++ {
		num, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		otp += num.String()
	}
	return otp, nil
}
```

- [ ] **Step 2: Update the two call sites in auth_handlers.go**

In `internal/handlers/auth_handlers.go`:

- Line ~136: change
  ```go
  if _, err := h.otpService.GenerateOTP(phoneNumber); err != nil {
  ```
  to
  ```go
  if _, err := h.otpService.GenerateOTP(r.Context(), phoneNumber); err != nil {
  ```

- Line ~174: change
  ```go
  valid, err := h.otpService.VerifyOTP(phoneNumber, otp)
  ```
  to
  ```go
  valid, err := h.otpService.VerifyOTP(r.Context(), phoneNumber, otp)
  ```

If the handler at either site uses a variable other than `r` for the `*http.Request`, use that variable's `.Context()` method instead. Confirm by reading the surrounding handler function.

- [ ] **Step 3: Build the module**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go build ./...`
Expected: exits 0, no output.

- [ ] **Step 4: Vet the module**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go vet ./...`
Expected: exits 0, no output.

- [ ] **Step 5: Run tests**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go test ./...`
Expected: all tests pass.

- [ ] **Step 6: Sanity — no more context.Background in otp_service.go**

Run: `cd /Users/shivangawasthi/bunzo/qcom && grep -n 'context\.Background\|context\.TODO' internal/service/otp_service.go`
Expected: empty output.

- [ ] **Step 7: Commit**

```bash
cd /Users/shivangawasthi/bunzo/qcom
git add internal/service/otp_service.go internal/handlers/auth_handlers.go
git commit -m "otp: take ctx + log start/done/duration on otp service calls"
```

---

### Task 5: Instrument external HTTP / S3 call sites

**Files:**
- `internal/service/geocoding_service.go` — instrument the `g.httpClient.Do(req)` call inside `ReverseGeocode`.
- `internal/service/eta_service.go` — instrument the Google Distance Matrix HTTP call inside `fetchDistanceMeters` (and any other external call in this file).
- `internal/service/upload_service.go` — instrument the S3 calls (presign / PutObject) inside `GeneratePresignedURL`.

Each external-call wrapper follows the per-call pattern but uses a layer label other than `dynamodb`. The instrumentation goes around the ACTUAL HTTP / S3 call, not around the whole method (top-level service instrumentation is in Task 6).

- [ ] **Step 1: Instrument geocoding_service.go**

In `internal/service/geocoding_service.go`, within `ReverseGeocode` (after building `req`, around the `g.httpClient.Do(req)` call), wrap:

```go
log := logging.FromContext(ctx, g.logger)
extStart := time.Now()
log.WithFields(logrus.Fields{
    "op":  "ReverseGeocode",
    "lat": lat,
    "lng": lng,
}).Info("google_geocode call start")

resp, err := g.httpClient.Do(req)
if err != nil {
    log.WithError(err).WithFields(logrus.Fields{
        "op":          "ReverseGeocode",
        "duration_ms": time.Since(extStart).Milliseconds(),
    }).Error("google_geocode call failed")
    return "", fmt.Errorf("geocode request failed: %w", err)
}
defer resp.Body.Close()

log.WithFields(logrus.Fields{
    "op":          "ReverseGeocode",
    "status_code": resp.StatusCode,
    "duration_ms": time.Since(extStart).Milliseconds(),
}).Info("google_geocode call done")
```

Add `time` and `logging` imports if not already present. Keep the existing post-call status-code handling unchanged.

- [ ] **Step 2: Instrument eta_service.go**

In `internal/service/eta_service.go`, locate the Google Distance Matrix HTTP call (inside `fetchDistanceMeters`). Around the `httpClient.Do(req)` (or equivalent), apply the same pattern:

```go
log := logging.FromContext(ctx, s.logger)
extStart := time.Now()
log.WithFields(logrus.Fields{
    "op":         "fetchDistanceMeters",
    "origin":     fmt.Sprintf("%f,%f", originLat, originLng),
    "destination": fmt.Sprintf("%f,%f", destLat, destLng),
}).Info("google_distance_matrix call start")

resp, err := /* existing client.Do call */
if err != nil {
    log.WithError(err).WithFields(logrus.Fields{
        "op":          "fetchDistanceMeters",
        "duration_ms": time.Since(extStart).Milliseconds(),
    }).Error("google_distance_matrix call failed")
    return 0, /* existing wrapped error */
}
defer resp.Body.Close()

log.WithFields(logrus.Fields{
    "op":          "fetchDistanceMeters",
    "status_code": resp.StatusCode,
    "duration_ms": time.Since(extStart).Milliseconds(),
}).Info("google_distance_matrix call done")
```

If the variable holding the http client is named differently (e.g. `s.httpClient`, `s.client`), use that name. Add the `time` and `logging` imports if missing.

- [ ] **Step 3: Instrument upload_service.go**

In `internal/service/upload_service.go`, inside `GeneratePresignedURL`, locate the S3 presign call (likely something like `s.presignClient.PresignPutObject(...)`). Wrap it:

```go
log := logging.FromContext(ctx, s.logger)
extStart := time.Now()
log.WithFields(logrus.Fields{
    "op":     "PresignPutObject",
    "bucket": s.cfg.Bucket, // or whatever the bucket reference is
    "key":    objectKey,    // use the actual computed key variable
}).Info("s3 call start")

req, err := /* existing presign call */
if err != nil {
    log.WithError(err).WithFields(logrus.Fields{
        "op":          "PresignPutObject",
        "duration_ms": time.Since(extStart).Milliseconds(),
    }).Error("s3 call failed")
    return nil, /* existing wrapped error */
}

log.WithFields(logrus.Fields{
    "op":          "PresignPutObject",
    "duration_ms": time.Since(extStart).Milliseconds(),
}).Info("s3 call done")
```

Use whichever variable names already exist for the bucket and key. If there are additional S3 calls in `upload_service.go` (PutObject, HeadObject, etc.), wrap each one the same way with an appropriate `op`. If the only call is the presign call, that's fine — just wrap it once.

- [ ] **Step 4: Build the module**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go build ./...`
Expected: exits 0, no output.

- [ ] **Step 5: Vet the module**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go vet ./...`
Expected: exits 0, no output.

- [ ] **Step 6: Run tests**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go test ./...`
Expected: all tests pass. The ETA tests in `internal/service/eta_service_test.go` are particularly relevant — they exercise the path that now has new log lines.

- [ ] **Step 7: Commit**

```bash
cd /Users/shivangawasthi/bunzo/qcom
git add internal/service/geocoding_service.go internal/service/eta_service.go internal/service/upload_service.go
git commit -m "service: log start/done/duration around external http and s3 calls"
```

---

### Task 6: Instrument top-level service entry points

**Files (modify each):**
- `internal/service/address_service.go` — methods: `CreateAddress`, `GetAddressByID`, `GetMyAddresses`, `GetSuggestedAddresses`, `RemoveAddress`, `UpdateReceiverDetails`
- `internal/service/serviceability_service.go` — methods: `CheckServiceability`
- `internal/service/de_service.go` — methods: `GetDE`, `Register`, `StartDuty`
- `internal/service/refresh_token_service.go` — methods: `Get`, `IsRevoked`, `Revoke`, `RevokeFamily`, `Store`
- `internal/service/eta_service.go` — method: `GetETA` (the public entry point; `fetchDistanceMeters` was instrumented in Task 5)
- `internal/service/upload_service.go` — methods: `GeneratePresignedURL` (the public entry point — its inner S3 call was instrumented in Task 5)

Skipped (per spec):
- `jwt_service.go` — pure compute / no I/O.
- `qr_service.go` — pure compute.
- `otp_service.go` — already instrumented in Task 4.

For EVERY listed method, add at the TOP of the function body:

```go
log := logging.FromContext(ctx, s.logger)
start := time.Now()
log.WithFields(logrus.Fields{
    "op": "<method name>",
    // identifying input fields
}).Info("service call start")
```

For every error-return path, log:

```go
log.WithError(err).WithFields(logrus.Fields{
    "op":          "<method name>",
    "duration_ms": time.Since(start).Milliseconds(),
}).Error("service call failed")
```

…then `return` with the original error.

For every successful-return path, log:

```go
log.WithFields(logrus.Fields{
    "op":          "<method name>",
    "duration_ms": time.Since(start).Milliseconds(),
}).Info("service call done")
```

…then `return` with the original values.

**Identifying-input fields per method** (apply to the start log line):

| File | Method | start fields |
| --- | --- | --- |
| address_service.go | CreateAddress | `"user_id": userID` |
| address_service.go | GetAddressByID | `"address_id": addressID, "user_id": userID` |
| address_service.go | GetMyAddresses | `"user_id": userID` |
| address_service.go | GetSuggestedAddresses | `"user_id": userID, "lat": lat, "lng": lng` |
| address_service.go | RemoveAddress | `"address_id": addressID, "user_id": userID` |
| address_service.go | UpdateReceiverDetails | `"address_id": addressID, "user_id": userID` |
| serviceability_service.go | CheckServiceability | `"user_id": userID, "lat": lat, "lng": lng` |
| de_service.go | GetDE | `"phone": phone` |
| de_service.go | Register | (no extra fields — Register's request struct is fine to leave off; if the struct has a `Phone` field, include `"phone": req.Phone`) |
| de_service.go | StartDuty | `"phone": dePhone` |
| refresh_token_service.go | Get | `"jti": jti` |
| refresh_token_service.go | IsRevoked | `"jti": jti` |
| refresh_token_service.go | Revoke | `"jti": jti` |
| refresh_token_service.go | RevokeFamily | `"family_id": familyID` |
| refresh_token_service.go | Store | `"jti": jti, "entity_id": entityID, "entity_type": entityType` (do NOT log the phone or the token) |
| eta_service.go | GetETA | `"lat": userLat, "lng": userLng, "darkstore_id": darkstore.DarkstoreID` (if that field name doesn't exist, omit the darkstore_id field — only `lat`/`lng` are required) |
| upload_service.go | GeneratePresignedURL | `"user_id": userID, "file_name": fileName, "file_type": fileType, "file_size": fileSize` |

**Special note on `serviceability_service.go`:** the existing `s.logger.Warn("IS_TEST is set; bypassing serviceability polygon check")` line (added in a previous PR) MUST be changed to use the new `log` variable so it carries the trace ID. Change `s.logger.Warn(...)` to `log.Warn(...)`.

The existing `s.logger.WithError(err).Warn("ETA calculation failed; returning serviceable result without ETA")` and `s.logger.WithError(err).Warn("Reverse geocoding failed; ...")` lines must similarly switch to `log.WithError(err).Warn(...)` where `log` is the contextual entry for that method (note `resolveFromGeocode` also takes ctx — declare its own `log` at the top).

- [ ] **Step 1: Apply the pattern to every method listed**

Edit each service file according to the per-method table and the rules above. Add the `time` and `internal/logging` imports if missing. For each multi-return function, ensure EVERY exit path (including bare `return` and `return errors.New(...)`) is covered by either a done-line or a failed-line.

- [ ] **Step 2: Build the module**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go build ./...`
Expected: exits 0, no output.

- [ ] **Step 3: Vet the module**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go vet ./...`
Expected: exits 0, no output.

- [ ] **Step 4: Run tests**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go test ./...`
Expected: all tests pass.

- [ ] **Step 5: Sanity check — no orphan s.logger.WithError / s.logger.Warn in the instrumented services**

Run:
```bash
cd /Users/shivangawasthi/bunzo/qcom
grep -nE 's\.logger\.(WithError|Warn|Info|Error)' \
    internal/service/address_service.go \
    internal/service/serviceability_service.go \
    internal/service/de_service.go \
    internal/service/refresh_token_service.go \
    internal/service/eta_service.go \
    internal/service/upload_service.go
```
Expected: empty output. Every previous direct use of `s.logger` inside these files should have been replaced by the contextual `log` variable.

If this grep returns lines, audit each one — anything inside a method that has access to `ctx` should be using `log`, not `s.logger`. The base `s.logger` is reserved for code paths without a ctx (which, in these files, after instrumentation, is none).

- [ ] **Step 6: Smoke test — confirm trace ID flows through a real request**

```bash
cd /Users/shivangawasthi/bunzo/qcom
JWT_SECRET_KEY=test-secret-keys-are-at-least-32-bytes-long IS_TEST=true go run ./cmd/server 2>&1 | tee /tmp/qcom-server.log &
SERVER_PID=$!
sleep 2

curl -sS -H 'X-Trace-Id: smoke-test-trace-001' http://localhost:8080/health > /dev/null

kill $SERVER_PID
sleep 1
grep 'smoke-test-trace-001' /tmp/qcom-server.log | head -5 || echo "NO TRACE IN LOGS"
```

Expected: the grep finds at least the "HTTP request" log line carrying `trace_id=smoke-test-trace-001`. (The `/health` endpoint may not hit any DB/service, so the trace-id will only appear on the request log line — that's fine. The point of this smoke test is to confirm end-to-end propagation, not to exercise every layer.)

If you have a richer endpoint that hits DynamoDB available without auth, hit that instead and confirm `trace_id` appears on the DB log lines too.

- [ ] **Step 7: Commit**

```bash
cd /Users/shivangawasthi/bunzo/qcom
git add internal/service/address_service.go internal/service/serviceability_service.go internal/service/de_service.go internal/service/refresh_token_service.go internal/service/eta_service.go internal/service/upload_service.go
git commit -m "service: log start/done/duration on top-level service entry points"
```

---

## Final Verification

After the last task commits, verify the branch as a whole:

- [ ] `cd /Users/shivangawasthi/bunzo/qcom && go build ./... && go vet ./... && go test ./...` — all clean.
- [ ] `git log --oneline main..HEAD` — six commits, one per task above.
- [ ] `grep -rn 'context\.Background' internal/service/ | grep -v _test.go` — empty (no production code path drops the trace ID).
- [ ] `grep -rn 'trace_id' internal/ | wc -l` — non-zero (sanity that the field made it into the codebase).

## Self-Review Notes

- **Spec coverage:** Logging package (Task 1), trace middleware + updated request log (Task 2), repos (Task 3), OTP signature fix + instrumentation (Task 4), external HTTP/S3 (Task 5), top-level services + the IS_TEST warn fix (Task 6). The spec's "in-scope" list is fully covered. Out-of-scope items (Redis, JWT pure-compute, restructuring of `*logrus.Logger` injection) are intentionally not addressed.
- **Placeholder scan:** No TBDs. Each task lists exact files and exact methods. Per-method identifying fields are tabulated rather than left as "use sensible fields." When a model field may or may not exist (e.g., DeliveryExecutive's `Phone`), the plan explicitly says "if available; otherwise omit."
- **Type consistency:** `logging.FromContext`, `logging.WithTraceID`, `logging.TraceIDFromContext` are used with identical signatures across all tasks. The layer-label vocabulary (`dynamodb`, `google_geocode`, `google_distance_matrix`, `s3`, `service`) is fixed in the conventions block and reused verbatim.
- **OTPService signature change risk:** Two call sites in `auth_handlers.go`. Step 2 of Task 4 names them explicitly with the surrounding code. No other callers exist (verified by grep during plan authoring).
