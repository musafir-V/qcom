# Driver Drop-Deadline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Freeze a display-only driver drop-task countdown at pickup complete (accepted → out_for_delivery) as `drop_deadline` epoch seconds on `GET /api/v1/de/trip`. Admin-configurable x/y: minutes = `distance_km * minutes_per_km + extra_minutes`. Changing x/y never moves in-flight deadlines. GET never recomputes.

**Architecture:** Copy the drop-reached CONFIG singleton (`internal/repository/trip_reached_config_repository.go` + `internal/handlers/admin_trip_reached_handlers.go`), NOT payout PATCH, NOT assignment config. New Dynamo item `PK=CONFIG SK=DROP_DEADLINE_V1` holds `minutes_per_km` (x, default 2) and `extra_minutes` (y, default 0). At pickup complete, `onTaskCompleted` computes `now + (distance_km * x + y)` minutes once, writes it on the trip as a Dynamo Number, and `GET /de/trip` returns that stored int. No DTO. App UI out of scope for T1–T5. Do not touch payout / on_time / SLAMinutes / customer ETA.

**Tech Stack:** Go 1.25, gorilla/mux, aws-sdk-go-v2 DynamoDB, stdlib testing + httptest (NO testify).

## Global Constraints

- Do NOT invent APIs. Do NOT touch admin-dashboard in T1–T5. T6/T7 are notes only for later repos.
- Do NOT modify: `internal/service/payout_service.go`, `computeCompletionPayout`, `UpdatePayout`, `recordTripPayout`, `SLAMinutes` writes, `OnTime`, `internal/service/eta_service.go`, `AcceptDeadline`.
- Rounding: `now.Add(time.Duration((distanceKM*x + y) * float64(time.Minute))).Unix()` — do NOT ceil.
- Times today are RFC3339; this field is epoch seconds `int64`. Omit key before pickup complete (`*int64` + `omitempty`).
- JSON admin body: `{"minutes_per_km": <x>, "extra_minutes": <y>}`. Both required on PATCH. `minutes_per_km` must be `> 0`. `extra_minutes` must be `>= 0` (0 is valid).
- Customer ETA (`ceil(km*2)+3`) and payout SLA (`MinutesPerKm` default 4) are different formulas. Do not reuse them.
- Admin routes: `GET+PATCH /api/v1/admin/config/drop-deadline` behind `RequireAdminAuth`. Copy from `/api/v1/admin/config/drop-reached`.
- TDD: no production code without a failing test first. Watch each test fail for the right reason, then write minimal code. Cover at least 85% of new code.
- NewTripService: T4 does not change the ctor signature (tests set the struct field). T5 adds `dropDeadlineConfig` after `reachedConfig`.
- Do not commit secrets, binaries, or unrelated untracked files.

## Locked qcom contract

```
GET/PATCH /api/v1/admin/config/drop-deadline
  {"minutes_per_km": <x>, "extra_minutes": <y>}

GET /api/v1/de/trip
  drop_deadline  epoch seconds int, omitempty before pickup complete
```

## File structure

**qcom (T1–T5, this PR)**
- Create: `internal/models/drop_deadline_config.go`, `internal/models/drop_deadline_config_test.go`
- Create: `internal/repository/drop_deadline_config_repository.go`
- Create: `internal/handlers/admin_drop_deadline_handlers.go`, `internal/handlers/admin_drop_deadline_handlers_test.go`
- Modify: `internal/models/trip.go` — `DropDeadline *int64` immediately after `AcceptDeadline`
- Modify: `internal/models/trip_test.go`
- Modify: `internal/repository/trip_repository.go` — `MarkOutForDelivery`
- Modify: `internal/service/trip_service.go` — `tripRepoI`, `dropDeadlineConfigStore`, pickup branch of `onTaskCompleted`, T5 ctor
- Modify: `internal/service/trip_service_test.go`
- Modify: `cmd/server/main.go` (T5 only)
- Modify: `tests/integration/upload_api_test.go` (NewTripService arity)

**admin-dashboard (T6 notes — separate repo, not this PR)**
- Modify: `src/lib/api.ts`, `src/lib/types.ts`, Rider Settings page

**driver app (T7 notes — out of scope)**
- Read `drop_deadline` epoch seconds from `GET /api/v1/de/trip`; countdown UI only

## Parallel waves (same branch, independent files first)

- Wave 1: T1 || T2
- Wave 2: T3 (needs T1) || T4 (needs T1)
- Wave 3: T5 (needs T3+T4)
- Later: T6 (admin-dashboard) || T7 (driver app) — not on this qcom PR

---

### Task 1: Drop-deadline config + freeze formula

**Files:**
- Create: `internal/models/drop_deadline_config.go`
- Create: `internal/models/drop_deadline_config_test.go`

**Interfaces:**
- Produces: `DefaultDropDeadlineMinutesPerKm = 2.0`, `DefaultDropDeadlineExtraMinutes = 0.0`
- Produces: `type DropDeadlineConfig struct { MinutesPerKm float64 \`json:"minutes_per_km" dynamodbav:"minutes_per_km"\`; ExtraMinutes float64 \`json:"extra_minutes" dynamodbav:"extra_minutes"\` }`
- Produces: `GetPK() -> "CONFIG"`, `GetSK() -> "DROP_DEADLINE_V1"`
- Produces: `EffectiveMinutesPerKm()` — nil or `<= 0` then 2
- Produces: `EffectiveExtraMinutes()` — nil or `< 0` then 0; stored 0 stays 0
- Produces: `ComputeDropDeadlineUnix(now time.Time, distanceKM, minutesPerKm, extraMinutes float64) int64`

- [x] **Step 1: Write the failing tests**

```go
package models
import ("testing"; "time")
func TestDropDeadlineConfigKeys(t *testing.T) {
	c := &DropDeadlineConfig{}
	if c.GetPK() != "CONFIG" || c.GetSK() != "DROP_DEADLINE_V1" { t.Fatalf("keys = %s/%s", c.GetPK(), c.GetSK()) }
}
func TestEffectiveMinutesPerKm_DefaultWhenNilOrNonPositive(t *testing.T) {
	if (*DropDeadlineConfig)(nil).EffectiveMinutesPerKm() != 2 { t.Fatal("nil") }
	if (&DropDeadlineConfig{}).EffectiveMinutesPerKm() != 2 { t.Fatal("zero") }
	if (&DropDeadlineConfig{MinutesPerKm: -1}).EffectiveMinutesPerKm() != 2 { t.Fatal("neg") }
}
func TestEffectiveMinutesPerKm_Positive(t *testing.T) {
	if (&DropDeadlineConfig{MinutesPerKm: 3.5}).EffectiveMinutesPerKm() != 3.5 { t.Fatal() }
}
func TestEffectiveExtraMinutes_ZeroIsValid(t *testing.T) {
	if (*DropDeadlineConfig)(nil).EffectiveExtraMinutes() != 0 { t.Fatal("nil") }
	if (&DropDeadlineConfig{ExtraMinutes: 0}).EffectiveExtraMinutes() != 0 { t.Fatal("0") }
	if (&DropDeadlineConfig{ExtraMinutes: 5}).EffectiveExtraMinutes() != 5 { t.Fatal("5") }
	if (&DropDeadlineConfig{ExtraMinutes: -4}).EffectiveExtraMinutes() != 0 { t.Fatal("neg") }
}
func TestComputeDropDeadlineUnix_FormulaNoCeil(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	got := ComputeDropDeadlineUnix(now, 3.2, 2, 0) // 6.4 min = 384s, do not ceil to 7
	want := now.Add(6*time.Minute + 24*time.Second).Unix()
	if got != want { t.Fatalf("got %d, want %d", got, want) }
}
func TestComputeDropDeadlineUnix_UsesXAndY(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	got := ComputeDropDeadlineUnix(now, 2, 3, 4) // 10 min
	if got != now.Add(10*time.Minute).Unix() { t.Fatal() }
}
func TestComputeDropDeadlineUnix_NotCustomerETA(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	// Customer ETA would be ceil(0.5*2)+3 = 4 min. Driver timer is 0.5*2+0 = 1 min.
	got := ComputeDropDeadlineUnix(now, 0.5, 2, 0)
	if got != now.Add(time.Minute).Unix() { t.Fatal("must not be ETA+pack") }
}
```

- [x] **Step 2: Run tests — expect compile FAIL** (`undefined: DropDeadlineConfig`)

Run: `go test -v ./internal/models/ -run 'DropDeadline|ComputeDropDeadline'`

- [x] **Step 3: Implement**

```go
package models
import "time"
const (
	DefaultDropDeadlineMinutesPerKm = 2.0
	DefaultDropDeadlineExtraMinutes = 0.0
)
type DropDeadlineConfig struct {
	MinutesPerKm float64 `json:"minutes_per_km" dynamodbav:"minutes_per_km"`
	ExtraMinutes float64 `json:"extra_minutes" dynamodbav:"extra_minutes"`
}
func (c *DropDeadlineConfig) GetPK() string { return "CONFIG" }
func (c *DropDeadlineConfig) GetSK() string { return "DROP_DEADLINE_V1" }
func (c *DropDeadlineConfig) EffectiveMinutesPerKm() float64 {
	if c == nil || c.MinutesPerKm <= 0 { return DefaultDropDeadlineMinutesPerKm }
	return c.MinutesPerKm
}
func (c *DropDeadlineConfig) EffectiveExtraMinutes() float64 {
	if c == nil || c.ExtraMinutes < 0 { return DefaultDropDeadlineExtraMinutes }
	return c.ExtraMinutes
}
func ComputeDropDeadlineUnix(now time.Time, distanceKM, minutesPerKm, extraMinutes float64) int64 {
	mins := distanceKM*minutesPerKm + extraMinutes
	if mins < 0 { mins = 0 }
	return now.Add(time.Duration(mins * float64(time.Minute))).Unix()
}
```

- [x] **Step 4: Run tests — expect PASS**

- [x] **Step 5: Commit** `feat: add drop-deadline config defaults and freeze formula`

---

### Task 2: Trip.DropDeadline JSON (epoch seconds)

**Files:**
- Modify: `internal/models/trip.go` (immediately after `AcceptDeadline`)
- Modify: `internal/models/trip_test.go`

**Interfaces:**
- Produces: `DropDeadline *int64 \`json:"drop_deadline,omitempty" dynamodbav:"drop_deadline,omitempty"\``
- `GET /api/v1/de/trip` already encodes `*Trip` — field appears automatically.

- [x] **Step 1: Write failing tests** (`TestTripJSON_DropDeadlineOmittedWhenNil`, `TestTripJSON_DropDeadlineEpochSecondsNumber`, `TestTripDynamo_DropDeadlineNumber`)

- [x] **Step 2: Run — expect FAIL** (`unknown field DropDeadline`)

Run: `go test -v ./internal/models/ -run 'TripJSON_DropDeadline|TripDynamo_DropDeadline'`

- [x] **Step 3: Add field after AcceptDeadline**

- [x] **Step 4: Run — expect PASS**

- [x] **Step 5: Commit** `feat: add trip.drop_deadline as epoch seconds`

---

### Task 3: Admin GET/PATCH `/api/v1/admin/config/drop-deadline`

**Files:**
- Create: `internal/repository/drop_deadline_config_repository.go` (copy `trip_reached_config_repository.go`; Get on missing item returns `&models.DropDeadlineConfig{}, nil`)
- Create: `internal/handlers/admin_drop_deadline_handlers.go`
- Create: `internal/handlers/admin_drop_deadline_handlers_test.go`
- Do NOT touch `cmd/server/main.go` (T5 wires routes).

**Interfaces:**
- Produces: `NewAdminDropDeadlineHandlers` / `newAdminDropDeadlineHandlers` (unexported ctor for tests with interface)
- Produces: `dropDeadlineConfigStore interface { Get; Put }`
- GetConfig returns effective values (x=2 y=0 when missing)
- PatchConfig: both fields required as pointers; reject missing, x<=0, y<0; allow y=0
- Error envelope: `{error:{code,message}}`

- [x] **Step 1: Write failing handler tests first** (stdlib httptest, stub store)

Tests: `TestGetDropDeadline_Default`, `TestPatchDropDeadline_Success`, `TestPatchDropDeadline_AllowsZeroY`, `TestPatchDropDeadline_RejectsMissingFields`, `TestPatchDropDeadline_RejectsNonPositiveX`, `TestPatchDropDeadline_RejectsNegativeY`

- [x] **Step 2: Run — expect compile FAIL** (`undefined: AdminDropDeadlineHandlers`)

Run: `go test -v ./internal/handlers/ -run DropDeadline`

- [x] **Step 3: Implement repo + handlers**

- [x] **Step 4: Run tests + coverage** (`go test -cover ./internal/handlers/ ./internal/models/`). New handler + model files at least 85% covered.

- [x] **Step 5: Commit** `feat: admin GET/PATCH drop-deadline config`

---

### Task 4: Freeze drop_deadline at pickup complete

**Files:**
- Modify: `internal/repository/trip_repository.go` — add `MarkOutForDelivery`
- Modify: `internal/service/trip_service.go` — `tripRepoI`, `dropDeadlineConfigStore`, TripService field, pickup branch of `onTaskCompleted`
- Modify: `internal/service/trip_service_test.go` — stub method + freeze tests

**Interfaces:**
- Ctor rule: Do NOT change `NewTripService` signature in this task. Add `dropDeadlineConfig dropDeadlineConfigStore` on the struct. Tests set the field on the literal. T5 injects production config.
- Produces: `MarkOutForDelivery(ctx, tripID string, dropDeadline int64) error`
- `TripRepository.MarkOutForDelivery`: one UpdateItem `SET #status = :status, updated_at = :now, drop_deadline = :dd` with status `out_for_delivery`, `:dd` Number (`strconv.FormatInt`).
- Pickup `onTaskCompleted`: replace `UpdateStatus` with `MarkOutForDelivery` after computing deadline via `ComputeDropDeadlineUnix(timezone.Now(), trip.DistanceKM, cfg.EffectiveMinutesPerKm(), cfg.EffectiveExtraMinutes())`. If config get fails, log and use defaults (`cfg=nil`). Keep Java sync + `notifyCustomer`.
- Add `MarkOutForDelivery` to ALL `tripRepoI` stubs in `*_test.go`.

- [x] **Step 1: Write failing tests** (`stubTripRepo.MarkOutForDelivery` sets `updateStatusCalled` + `dropDeadline`)

Tests: `TestUpdateTaskStatus_PickupCompletion_NotifiesCustomer` must still pass; `TestUpdateTaskStatus_PickupCompletion_FreezesDropDeadline`; `TestUpdateTaskStatus_PickupCompletion_UsesConfigXY`; `TestGetCurrentTrip_ReturnsStoredDropDeadlineNotRecomputed`

Use `timezone.Now()` for bounds. `newTripServiceForTest` already exists — do not invent a different test helper.

- [x] **Step 2: Run — expect FAIL** (`dropDeadline = 0`)

Run: `go test -v ./internal/service/ -run 'PickupCompletion|ReturnsStoredDropDeadline|DropCompletion_Notifies'`

- [x] **Step 3: Implement freeze write**

- [x] **Step 4: Run — expect PASS**

- [x] **Step 5: Commit** `feat: freeze drop_deadline when pickup completes`

---

### Task 5: Wire `cmd/server/main.go`

**Files:**
- Modify: `cmd/server/main.go`
- Modify: `internal/service/trip_service.go` — extend `NewTripService` with `dropDeadlineConfig` after `reachedConfig`
- Modify: any remaining compile-broken `NewTripService` call sites / `tripRepoI` stubs

**Interfaces:**
- `dropDeadlineConfigRepo := repository.NewDropDeadlineConfigRepository(...)`
- pass into `NewTripService`
- `adminDropDeadlineHandlers := handlers.NewAdminDropDeadlineHandlers(...)`
- add `setupRouter` param after `adminTripReachedHandlers`
- routes next to drop-reached:

```go
admin.HandleFunc("/config/drop-deadline", adminDropDeadlineHandlers.GetConfig).Methods("GET", "OPTIONS")
admin.HandleFunc("/config/drop-deadline", adminDropDeadlineHandlers.PatchConfig).Methods("PATCH", "OPTIONS")
```

- [x] **Step 1: Extend NewTripService — expect `go build ./cmd/server` FAIL** (not enough arguments)

- [x] **Step 2: Wire repo, ctor, admin handlers, routes**

- [x] **Step 3: Verify**

```
go build -o /tmp/qcom ./cmd/server
go test ./internal/models/ ./internal/handlers/ ./internal/service/
```

- [x] **Step 4: Commit** `feat: wire drop-deadline admin routes and trip-service config`

---

### Task 6 notes: Admin dashboard Rider Settings (separate repo)

**Repo:** admin-dashboard (NOT this qcom PR). Do not implement here.

**Intent:** Surface x/y next to drop-reached on Rider Settings.

**Locked API (already on qcom):**
- `GET /api/v1/admin/config/drop-deadline` → `{minutes_per_km, extra_minutes}` (effective values)
- `PATCH /api/v1/admin/config/drop-deadline` → same body; both required; x>0; y>=0

**Files (when scheduled):**
- Modify: `src/lib/types.ts` — `DropDeadlineConfig { minutes_per_km: number; extra_minutes: number }`
- Modify: `src/lib/api.ts` — `getDropDeadlineConfig` / `patchDropDeadlineConfig` mirroring drop-reached
- Modify: Rider Settings page — number inputs for x and y; Save PATCHes both fields; copy that changing x/y does not move in-flight driver timers

**Constraints:** Copy drop-reached client patterns. Do not invent a new path. Do not call payout config.

---

### Task 7 notes: Driver app countdown (separate repo)

**Repo:** driver / rider app (NOT this qcom PR). Do not implement here.

**Locked API (already on qcom):**
- `GET /api/v1/de/trip` includes `drop_deadline` as a JSON number (epoch seconds) after pickup complete
- Key is omitted (`omitempty`) before pickup complete — treat missing as “no countdown”
- Do not recompute from distance or admin x/y. Display the stored epoch only.

**Constraints:** Display-only. Do not send `drop_deadline` on task-status writes. Do not confuse with customer ETA or accept_deadline (RFC3339 string).

---

## Done when (this qcom PR)

- T1–T5 implemented TDD, all specified tests pass
- Plan file saved at `docs/superpowers/plans/2026-08-29-driver-drop-deadline.md`
- `go test ./internal/models/ ./internal/handlers/ ./internal/service/` green
- `go build ./cmd/server` succeeds
- ≥85% coverage on new files (`drop_deadline_config.go`, `admin_drop_deadline_handlers.go`)
- One PR against main with the locked contract in the description
