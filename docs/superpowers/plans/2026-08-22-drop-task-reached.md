# Drop-task reached Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add drop-task `reached` (driver at customer) as a task status with a soft haversine warning, a compat flag so old driver apps still complete from `created`, and an admin Rider Settings page to roll the flag out.

**Architecture:** Reached is `TaskStatus` on the drop task, not a trip status. Rider `POST .../task/{taskId}/status/update` with `status=reached` plus lat/lng always succeeds (too-far is a warning). Complete still requires OTP. Admin Mark Delivered stays one call and internally applies `created → reached → completed`. Config lives in DynamoDB `PK=CONFIG SK=TRIP_REACHED_V1`.

**Tech Stack:** Go (qcom), DynamoDB, existing `models.HaversineDistance`, Next.js admin-dashboard.

## Global Constraints

- Trip statuses stay `created | assigned | accepted | out_for_delivery | completed | cancelled`. No trip-level `reached`.
- Drop task: `created → reached → completed`. Pickup: `created → completed` only; pickup `reached` is HTTP 200 no-op (no mutation, lat/lng ignored).
- Persist `reached_at` (RFC3339). Do not persist driver coords or distance.
- Soft geofence: too-far still 200 and sets reached. Missing/invalid **driver** lat/lng on drop reached → 400. Missing/zero **customer** coords → still 200, log no-compare, omit distance fields.
- Default `radius_meters=150`. Default `require_reached_before_complete=false` (old apps keep `created → completed`).
- Flag on: rider drop complete from `created` → 400 `DROP_NOT_REACHED`. Pickup complete unchanged.
- OTP stays on complete. No customer push and no Java sync on reached.
- Admin complete skips geofence and OTP; internally writes reached then completed in one request; do not overwrite existing `reached_at`.
- Reassign does not reset drop `reached`.
- Track API: show OTP while drop is `created` **or** `reached`.
- Driver app is out of scope. Admin dashboard Rider Settings (`/riders/settings`) is in scope.
- TDD: failing test first, watch it fail, then implement. Follow existing stub patterns in `internal/service/trip_service_test.go` and `internal/handlers/admin_sms_otp_routing_handlers_test.go`.
- Do not commit secrets, binaries, or unrelated untracked files. Do not edit files under other worktrees.
- Nil `reachedConfig` on `TripService` must apply the same defaults as a missing Dynamo row so existing unit tests keep passing.

## File structure

**qcom**
- Create: `internal/models/trip_reached_config.go`, `internal/models/trip_reached_config_test.go`
- Create: `internal/repository/trip_reached_config_repository.go`
- Create: `internal/handlers/admin_trip_reached_handlers.go`, `internal/handlers/admin_trip_reached_handlers_test.go`
- Modify: `internal/models/trip.go` (`TaskStatusReached`, `Task.ReachedAt`)
- Modify: `internal/service/trip_service.go` (reached path, transition rules, admin complete)
- Modify: `internal/service/trip_service_test.go`
- Modify: `internal/handlers/trip_handlers.go` (lat/lng body, reached response, error codes)
- Modify: `internal/handlers/trip_handlers_test.go`
- Modify: `internal/handlers/track_handlers.go` + `_test.go` (OTP while reached)
- Modify: `cmd/server/main.go` (wire repo + routes)

**admin-dashboard** (`/Users/shivangawasthi/bunzo/admin-dashboard`)
- Modify: `src/lib/navConfig.ts`, `src/lib/api.ts`, `src/lib/types.ts`
- Create: `src/app/riders/settings/page.tsx`

---

### Task 1: TripReachedConfig model

**Files:**
- Create: `internal/models/trip_reached_config.go`
- Create: `internal/models/trip_reached_config_test.go`

**Interfaces:**
- Produces: `TripReachedConfig`, `DefaultReachedRadiusMeters = 150`, `EffectiveRadiusMeters()`, `RequireReached()`, `GetPK()=CONFIG`, `GetSK()=TRIP_REACHED_V1`

- [ ] **Step 1: Write the failing tests**

```go
package models

import "testing"

func TestEffectiveRadiusMeters_DefaultWhenZero(t *testing.T) {
	c := &TripReachedConfig{}
	if got := c.EffectiveRadiusMeters(); got != DefaultReachedRadiusMeters {
		t.Fatalf("got %v, want %v", got, DefaultReachedRadiusMeters)
	}
}

func TestEffectiveRadiusMeters_NegativeUsesDefault(t *testing.T) {
	c := &TripReachedConfig{RadiusMeters: -1}
	if got := c.EffectiveRadiusMeters(); got != DefaultReachedRadiusMeters {
		t.Fatalf("got %v, want %v", got, DefaultReachedRadiusMeters)
	}
}

func TestEffectiveRadiusMeters_Positive(t *testing.T) {
	c := &TripReachedConfig{RadiusMeters: 200}
	if got := c.EffectiveRadiusMeters(); got != 200 {
		t.Fatalf("got %v, want 200", got)
	}
}

func TestRequireReached_DefaultFalse(t *testing.T) {
	c := &TripReachedConfig{}
	if c.RequireReached() {
		t.Fatal("expected false")
	}
}

func TestRequireReached_True(t *testing.T) {
	c := &TripReachedConfig{RequireReachedBeforeComplete: true}
	if !c.RequireReached() {
		t.Fatal("expected true")
	}
}

func TestTripReachedConfigKeys(t *testing.T) {
	c := &TripReachedConfig{}
	if c.GetPK() != "CONFIG" || c.GetSK() != "TRIP_REACHED_V1" {
		t.Fatalf("keys = %s/%s", c.GetPK(), c.GetSK())
	}
}
```

- [ ] **Step 2: Run tests — expect FAIL** (undefined `TripReachedConfig`)

Run: `go test ./internal/models -run 'TestEffectiveRadiusMeters|TestRequireReached|TestTripReachedConfigKeys' -count=1`

- [ ] **Step 3: Implement**

```go
package models

const DefaultReachedRadiusMeters = 150.0

// TripReachedConfig is PK=CONFIG SK=TRIP_REACHED_V1.
type TripReachedConfig struct {
	RadiusMeters                   float64 `json:"radius_meters" dynamodbav:"radius_meters"`
	RequireReachedBeforeComplete   bool    `json:"require_reached_before_complete" dynamodbav:"require_reached_before_complete"`
}

func (c *TripReachedConfig) GetPK() string { return "CONFIG" }
func (c *TripReachedConfig) GetSK() string { return "TRIP_REACHED_V1" }

func (c *TripReachedConfig) EffectiveRadiusMeters() float64 {
	if c == nil || c.RadiusMeters <= 0 {
		return DefaultReachedRadiusMeters
	}
	return c.RadiusMeters
}

func (c *TripReachedConfig) RequireReached() bool {
	if c == nil {
		return false
	}
	return c.RequireReachedBeforeComplete
}
```

- [ ] **Step 4: Re-run tests — expect PASS**

- [ ] **Step 5: Commit** `feat: add trip reached config model with 150m default`

---

### Task 2: Config repository + admin GET/PATCH

**Files:**
- Create: `internal/repository/trip_reached_config_repository.go` (mirror `sms_otp_routing_config_repository.go`: `Get` missing-row → zero value; `Put(ctx, *TripReachedConfig)` upserts PK/SK)
- Create: `internal/handlers/admin_trip_reached_handlers.go`
- Create: `internal/handlers/admin_trip_reached_handlers_test.go`
- Modify: `cmd/server/main.go` — construct repo; mount on admin router:

```
GET  /api/v1/admin/config/drop-reached
PATCH /api/v1/admin/config/drop-reached
```

**Interfaces:**
- Consumes: `models.TripReachedConfig` from Task 1
- Produces: JSON `{"radius_meters":150,"require_reached_before_complete":false}` using **effective** values on GET (never return 0 radius). PATCH body both fields required. PATCH then GET-shaped response with what was stored (radius must be > 0 or 400 `INVALID_REQUEST`).

- [ ] **Step 1: Write handler tests with a stub store** (copy `admin_sms_otp_routing_handlers_test.go` pattern)

Cover:
- GET missing config → 200 `{radius_meters: 150, require_reached_before_complete: false}`
- PATCH `{"radius_meters":200,"require_reached_before_complete":true}` → 200 echo, stub Put called
- PATCH missing field / radius <= 0 / invalid JSON → 400 `INVALID_REQUEST`
- GET repo error → 500 `CONFIG_FETCH_FAILED`
- PATCH repo error → 500 `CONFIG_UPDATE_FAILED`

JSON field names: `radius_meters`, `require_reached_before_complete`.

- [ ] **Step 2: Run tests — expect FAIL**

Run: `go test ./internal/handlers -run 'TripReached|DropReached' -count=1`

- [ ] **Step 3: Implement repo + handlers + wire routes** next to `admin.HandleFunc("/sms-otp-routing", ...)`

Handler GET must call `cfg.EffectiveRadiusMeters()` and `cfg.RequireReached()` so a missing row returns 150/false.

PATCH: both fields required (`RequireReachedBeforeComplete` via `*bool` and `RadiusMeters` via `*float64`). Reject `*radius <= 0`.

- [ ] **Step 4: Re-run handler tests — expect PASS**

- [ ] **Step 5: Commit** `feat: add admin GET/PATCH for drop-reached config`

---

### Task 3: TaskStatus reached + transition rules + track OTP

**Files:**
- Modify: `internal/models/trip.go` — add `TaskStatusReached TaskStatus = "reached"` and `ReachedAt string` on `Task` (`json:"reached_at,omitempty" dynamodbav:"reached_at,omitempty"`)
- Modify: `internal/service/trip_service.go` — `validateTaskTransition`
- Modify: `internal/service/trip_service_test.go`
- Modify: `internal/handlers/track_handlers.go` — OTP when drop is created **or** reached
- Modify: `internal/handlers/track_handlers_test.go`

**Interfaces:**
- `validateTaskTransition(task, newStatus, requireReached bool) error`
- Drop `created → reached` allowed
- Drop `reached → completed` allowed
- Drop `created → completed` allowed iff `!requireReached`
- Pickup `created → completed` allowed; pickup target `reached` is NOT validated here (service no-ops first)
- Non-drop/non-completed/non-reached targets still `ErrInvalidTransition`
- Already `completed` cannot transition
- Add sentinel `ErrDropNotReached`

- [ ] **Step 1: Write failing tests**

Update `TestValidateTaskTransition_CreatedToCompleted` to pass `requireReached=false`.

Add:

```go
func TestValidateTaskTransition_DropCreatedToReached(t *testing.T) {
	task := models.Task{Type: models.TaskTypeDrop, Status: models.TaskStatusCreated}
	if err := validateTaskTransition(task, models.TaskStatusReached, false); err != nil {
		t.Fatalf("expected created→reached, got %v", err)
	}
}

func TestValidateTaskTransition_DropReachedToCompleted(t *testing.T) {
	task := models.Task{Type: models.TaskTypeDrop, Status: models.TaskStatusReached}
	if err := validateTaskTransition(task, models.TaskStatusCompleted, true); err != nil {
		t.Fatalf("expected reached→completed, got %v", err)
	}
}

func TestValidateTaskTransition_DropCreatedToCompleted_AllowedWhenFlagOff(t *testing.T) {
	task := models.Task{Type: models.TaskTypeDrop, Status: models.TaskStatusCreated}
	if err := validateTaskTransition(task, models.TaskStatusCompleted, false); err != nil {
		t.Fatalf("compat path must allow created→completed, got %v", err)
	}
}

func TestValidateTaskTransition_DropCreatedToCompleted_RejectedWhenFlagOn(t *testing.T) {
	task := models.Task{Type: models.TaskTypeDrop, Status: models.TaskStatusCreated}
	err := validateTaskTransition(task, models.TaskStatusCompleted, true)
	if err == nil || !errors.Is(err, ErrDropNotReached) {
		t.Fatalf("got %v, want ErrDropNotReached", err)
	}
}
```

Keep `TestValidateTaskTransition_LegacyStatusToCompleted` working (`arrived`/`reached` → completed still ok, `requireReached` irrelevant for those sources).

Track test: copy `TestTrack_OutForDelivery_ShowsOTPNameETAAndPreservesOrder` with drop status `TaskStatusReached` — OTP must still be `"4321"`. Existing created-drop test must still pass.

- [ ] **Step 2: Run tests — expect FAIL**

Run: `go test ./internal/service -run 'TestValidateTaskTransition' -count=1` and `go test ./internal/handlers -run 'TestTrack_OutForDelivery' -count=1`

- [ ] **Step 3: Implement** `TaskStatusReached`, `ReachedAt`, `ErrDropNotReached`, new `validateTaskTransition` signature, update **all** existing `validateTaskTransition(` call sites to pass the flag. Track OTP:

```go
if drop := trip.DropTask(); drop != nil && (drop.Status == models.TaskStatusCreated || drop.Status == models.TaskStatusReached) {
    otp = &drop.OTP
}
```

- [ ] **Step 4: Re-run — expect PASS** (also `go test ./internal/service ./internal/handlers -count=1` to catch call-site compile errors)

- [ ] **Step 5: Commit** `feat: add drop task reached status and keep OTP visible`

---

### Task 4: TripService reached + admin through-reached

**Files:**
- Modify: `internal/service/trip_service.go`
- Modify: `internal/service/trip_service_test.go`

**Interfaces:**
- Add `reachedConfig reachedConfigStore` on `TripService` (`Get(ctx) (*models.TripReachedConfig, error)`). Nil repo → defaults.
- `UpdateTaskStatus` gains `lat, lng *float64` after photoS3Key. Return `(*TaskUpdateResult, error)`:

```go
type TaskUpdateResult struct {
    Status          string   `json:"status"`
    WithinRadius    *bool    `json:"within_radius,omitempty"`
    DistanceMeters  *float64 `json:"distance_meters,omitempty"`
    RadiusMeters    *float64 `json:"radius_meters,omitempty"`
}
```

Always set `Status: "updated"` on success.

**Reached behavior (drop):**
1. Same ownership / trip-not-closed / task-exists checks as complete.
2. `validateTaskAgainstTripStatus` — drop requires `out_for_delivery`.
3. If already `reached`: 200, do not change `reached_at`; still compute warning payload from new lat/lng.
4. If `completed`: `ErrInvalidTransition`.
5. Driver lat/lng required: nil → `ErrMissingLocation`; out of range / NaN / Inf → `ErrInvalidCoordinates`.
6. Customer lat/lng usable iff both not 0,0 (treat 0,0 as missing). If missing: log, set reached (if not already), return result **without** distance fields.
7. Else haversine via `models.HaversineDistance`; log `distance_meters`, `radius_meters`, `within_radius`, `trip_id` (not a persist). Always set reached even if outside radius.
8. First reached: `task.Status = reached`, `task.ReachedAt = timezone.Now().Format(RFC3339)`, `UpdateTasks`. Do **not** call `onTaskCompleted`, Java, or notifier.

**Pickup `newStatus == reached`:** return `&TaskUpdateResult{Status:"updated"}, nil` with no repo writes, ignore lat/lng.

**Complete:** load config; `validateTaskTransition(..., cfg.RequireReached())`; existing OTP/photo/Java/payout path. Return `&TaskUpdateResult{Status:"updated"}, nil`.

**AdminCompleteTask / AdminCompleteDropByOrder** for drop: if status is `created`, set `Status=reached` and `ReachedAt` if empty, then complete (skip OTP). If already `reached`, keep `ReachedAt`. Never call `validateTaskTransition` with requireReached=true for admin; after synthesizing reached, complete from `reached`. Pickup admin path unchanged.

Update every `UpdateTaskStatus(` test call to accept `(result, err)` and pass `nil, nil` for lat/lng on complete tests.

Stub `reachedConfig` in tests that need the flag on:

```go
type stubReachedConfig struct{ cfg *models.TripReachedConfig }
func (s stubReachedConfig) Get(context.Context) (*models.TripReachedConfig, error) { return s.cfg, nil }
```

Wire `updateTasksFn` to copy tasks onto `repo.trip.Tasks` so assertions can read status/`reached_at`.

- [ ] **Step 1: Write failing service tests**

Minimum cases:
- Drop reached within 150m, trip OFD, pickup done → status reached, reached_at set, UpdateTasks called, CompleteTripAndFreeDE not called, result.WithinRadius true
- Drop reached farther than 150m → still 200/reached, WithinRadius false, distance populated
- Drop reached missing lat → `ErrMissingLocation`, status stays created
- Drop reached customer 0,0 → reached set, distance fields nil
- Drop reached idempotent → second call keeps first `reached_at`
- Pickup reached → no UpdateTasks, pickup stays created
- Drop reached while trip `accepted` → `ErrPrerequisiteIncomplete`
- Drop complete from created with flag on → `ErrDropNotReached`
- Drop complete from created with flag off (default) → still completes (existing tests)
- Drop complete from reached + OTP → completes
- AdminCompleteTask drop from created → captured tasks have reached_at and status completed; no OTP
- AdminCompleteTask drop already reached → reached_at unchanged

- [ ] **Step 2: Run focused tests — expect FAIL**

Run: `go test ./internal/service -run 'Reached|AdminCompleteTask_Drop' -count=1`

- [ ] **Step 3: Implement** in `trip_service.go`. Keep `applyTaskCompletion` for completed only. New helper `applyDropReached`.

- [ ] **Step 4: `go test ./internal/service -count=1` PASS**

- [ ] **Step 5: Commit** `feat: mark drop reached with soft geofence and admin through-reached`

---

### Task 5: HTTP status/update contract

**Files:**
- Modify: `internal/handlers/trip_handlers.go`
- Modify: `internal/handlers/trip_handlers_test.go`

**Interfaces:**
- Body: `{ "status", "otp", "photo_s3_key", "lat", "lng" }` with `lat`/`lng` as `*float64`
- Success complete: `{"status":"updated"}`
- Success reached: `{"status":"updated", ...optional distance fields from TaskUpdateResult}`
- `classifyTaskUpdateError`: `ErrDropNotReached` → 400 `DROP_NOT_REACHED`; `ErrMissingLocation` → 400 `MISSING_LOCATION`; `ErrInvalidCoordinates` → 400 `INVALID_COORDINATES`

- [ ] **Step 1: Extend `TestClassifyTaskUpdateError`** with the three new sentinels. Add handler tests only if a testable handler constructor exists; otherwise classify tests + service coverage is enough. Prefer adding classify cases for sure.

- [ ] **Step 2: FAIL then implement handler decode + result JSON + classify mapping.** Update `UpdateTaskStatus` call to new signature.

- [ ] **Step 3: `go test ./internal/handlers ./internal/service -count=1` PASS**

- [ ] **Step 4: Commit** `feat: expose drop-reached on task status update API`

---

### Task 6: Admin dashboard Rider Settings

**Repo:** `/Users/shivangawasthi/bunzo/admin-dashboard`  
**Branch:** `feat/drop-task-reached`

**Files:**
- Modify: `src/lib/types.ts` — `TaskStatus` add `'reached'`; add `TripReachedConfig { radius_meters: number; require_reached_before_complete: boolean }`
- Modify: `src/lib/api.ts`:

```ts
getDropReachedConfig: () => request<TripReachedConfig>('/admin/config/drop-reached'),
patchDropReachedConfig: (body: TripReachedConfig) =>
  request<TripReachedConfig>('/admin/config/drop-reached', { method: 'PATCH', body }),
```

- Modify: `src/lib/navConfig.ts` — Riders section item `{ href: '/riders/settings', label: 'Rider Settings', icon: Settings2 }` after Payout Rules
- Create: `src/app/riders/settings/page.tsx` — client page matching payout-rules layout (`Card`, `useToast`, `ErrorBox`, `Loading` from `@/components/ui`):
  - Copy: distance is a warning, not a block; turning the toggle on breaks old driver apps that skip reached
  - Number input `radius_meters`
  - Checkbox/toggle `require_reached_before_complete`
  - Save calls PATCH
  - Turning toggle **from false to true** requires confirm: `Old driver apps will fail drop complete until they mark reached. Continue?`
  - Turning off: no confirm
  - CurrentTripCard: no `reached_at` display (status badge already shows `task.status`)

- [ ] **Step 1: Implement page + nav + API client** (no Go tests; typecheck)

Run: `npx tsc --noEmit` (or project equivalent)

- [ ] **Step 2: Commit** `feat: add Rider Settings page for drop-reached rollout`

---

## After all tasks (controller)

- Merge each feature branch into local `main`, push `main`.
- Deploy qcom to **us-east-1** test stack (same ASG name `qcom-asg`, override region/health):

```bash
set -a && source .deploy.local.env && set +a
AWS_REGION=us-east-1 QCOM_HEALTH_URL=https://api.test.bunzodelivery.com/health ./scripts/deploy.sh
curl -fsS https://api.test.bunzodelivery.com/health
```

Do **not** flip `require_reached_before_complete` (stays false). Do not deploy ap-southeast-2 production unless asked.
