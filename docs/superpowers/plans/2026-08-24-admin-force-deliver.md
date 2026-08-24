# Admin Force-Deliver Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Order-detail Mark Delivered closes a delivery from any stuck state: Java-only when there is no trip, assign-then-complete when a trip has no rider, force accept/pickup/drop when a rider is already on the trip.

**Architecture:** Extend `POST /api/v1/admin/orders/{orderId}/drop/complete` into a force-deliver (optional `driver_phone`). Add `GET /api/v1/admin/orders/{orderId}/drop/preview` so the confirm modal knows `mode` before the first POST. All trip side effects stay on `applyTaskCompletion` (COD, payout, push, Java sync). No-trip path writes Java only. Phone-scoped admin drop is unchanged.

**Tech Stack:** Go (qcom), DynamoDB stubs in `internal/service/trip_service_test.go`, Next.js admin-dashboard.

## Global Constraints

- Do not edit files under `qcom/.claude/worktrees/` or `qcom/.worktrees/` except the feature worktree created for this plan.
- Phone-scoped `AdminCompleteTask` / rider-page Mark Drop Done stay strict: drop still requires `out_for_delivery`.
- Order-page Mark Delivered is the only UI that uses force-deliver.
- Do not create a qcom trip when none exists.
- No trip + Java not `READY_FOR_DELIVERY` / `OUT_FOR_DELIVERY` → `blocked` / refuse. Do not walk `CONFIRMED`/`PACKING`.
- Java `CANCELLED` → refuse.
- Java already `DELIVERED` + open trip → still close the trip; do not POST Java `DELIVERED` again.
- Trip closed + Java `DELIVERED` → `already_done`.
- Assigned rider busy on a **different** trip (`current_order_id` set and ≠ this order) → refuse (`rider_busy_elsewhere`).
- `pick_rider` candidates: `assigned_store_id` = trip `store_id`, status in `{offline, eligible, free}`. Exclude `busy`. Include over-cash-limit riders. Show `in_hand_cash_zmw`.
- After force-deliver, restore prior DE status: was `offline` → `offline`; was `eligible`/`free` → leave `free` (normal drop-complete).
- POST body optional `{ "driver_phone": "+260…" }`. Required only for `pick_rider`. Ignored otherwise.
- Java actor remains `ADMIN:{username}` from JWT `entity_id`.
- TDD: failing test first, watch it fail, then implement. No production code without that.
- Do not add a JS test framework to admin-dashboard; extract a tiny helper and test it with `node --test`.

---

## File structure

| File | Responsibility |
|---|---|
| `internal/service/trip_service.go` | `PreviewAdminDropByOrder`, force-deliver `AdminCompleteDropByOrder`, new sentinels, `javaOrderAPI` interface |
| `internal/service/trip_service_test.go` | Unit tests + stub expansions |
| `internal/repository/de_repository.go` | `AttachToTrip` (sets busy + `current_order_id` + `current_trip_id`) |
| `internal/repository/de_repository_test.go` | Test for `AttachToTrip` if a DE repo test file exists; otherwise cover via service stubs |
| `internal/handlers/admin_driver_handlers.go` | GET preview + POST body `driver_phone` |
| `internal/handlers/trip_handlers.go` | Classify new sentinels in `classifyTaskUpdateError` |
| `cmd/server/main.go` | Register GET preview |
| `admin-dashboard/src/lib/types.ts` | Preview types |
| `admin-dashboard/src/lib/api.ts` | `getAdminDropPreview`, POST with optional phone |
| `admin-dashboard/src/lib/adminDropPreview.ts` | Pure modal/copy helper |
| `admin-dashboard/src/lib/adminDropPreview.test.mjs` | `node --test` |
| `admin-dashboard/src/app/orders/[orderNumber]/page.tsx` | Modal driven by preview |

### Task 1: Preview service

**Files:**
- Modify: `internal/service/trip_service.go`
- Modify: `internal/service/trip_service_test.go`
- Test: `go test ./internal/service -count=1 -run 'TestPreviewAdminDropByOrder'`

**Interfaces:**
- Consumes: existing `tripRepoI.GetByOrderID`, `deRepoI.GetByPhone`, `JavaOrderClient.GetOrderStatus`
- Produces:
  - `type javaOrderAPI interface { GetOrderStatus(ctx context.Context, orderID string) (string, error); UpdateOrderStatus(ctx context.Context, orderID, status, actorID string) error }`
  - Change `TripService.javaClient` field type from `*JavaOrderClient` to `javaOrderAPI` (`*JavaOrderClient` already satisfies it; `NewTripService` signature stays `*JavaOrderClient` which assigns to the interface).
  - `type AdminDropMode string` with values `java_only`, `pick_rider`, `force_progress`, `already_done`, `blocked`
  - `type AdminDropCandidate struct { Phone, Name, Status string; InHandCashZMW float64 }` JSON tags `phone`, `name`, `status`, `in_hand_cash_zmw`
  - `type AdminDropRider struct { Phone, Name, Status string }` JSON tags `phone`, `name`, `status`
  - `type AdminDropPreview struct { Mode AdminDropMode; Reason, TripID, JavaStatus string; Rider *AdminDropRider; Candidates []AdminDropCandidate }` JSON tags `mode`, `reason`, `trip_id`, `java_status`, `rider`, `candidates` (omit empty)
  - `func (s *TripService) PreviewAdminDropByOrder(ctx context.Context, orderID string) (*AdminDropPreview, error)`
  - Expand `deRepoI` with:
    - `UpdateStatus(ctx context.Context, phone string, status models.DEStatus, storeID, orderID string) error`
    - `AttachToTrip(ctx context.Context, phone, orderID, tripID, storeID string) error`
    - `ListByAssignedStore(ctx context.Context, indexKey, namePrefix, cursor string, limit int32) ([]*models.DeliveryExecutive, string, error)`
  - Expand `tripRepoI` with `AdminAssign(ctx context.Context, tripID, orderID, deID, dePhone, storeID string) error`
  - Expand `stubDERepo` / `stubTripRepo` so existing tests still compile (no-op / panic-on-unexpected for unused methods; `GetByPhone` unchanged)
  - New sentinels on `trip_service.go` error block:
    - `ErrOrderNotDeliverable = errors.New("order not deliverable")`
    - `ErrJavaOrderCancelled = errors.New("java order cancelled")`
    - `ErrRiderRequired = errors.New("rider required")`
    - `ErrRiderBusyElsewhere = errors.New("rider busy on another trip")`
    - `ErrAlreadyDelivered = errors.New("already delivered")`

Preview rules (implement exactly):

1. `javaStatus, err := s.javaClient.GetOrderStatus(ctx, orderID)` — if `javaClient` is nil, treat java status as `""` (tests that need Java set a stub). If GetOrderStatus returns `NOT_FOUND`, treat as `""`.
2. If `javaStatus == "CANCELLED"` → `{Mode: blocked, Reason: "java_cancelled", JavaStatus}` (even if a trip exists).
3. `trip := GetByOrderID`.
4. If `trip == nil` (or trip status is `completed` or `cancelled`):
   - If `javaStatus == "DELIVERED"` → `{Mode: already_done, JavaStatus}`
   - If `javaStatus == "READY_FOR_DELIVERY"` or `"OUT_FOR_DELIVERY"` → `{Mode: java_only, JavaStatus}`
   - Else → `{Mode: blocked, Reason: "java_not_ready", JavaStatus}`
5. If trip is open (`created`/`assigned`/`accepted`/`out_for_delivery`):
   - If `trip.DEPhone == ""` (unassigned / pooled `created`) → `{Mode: pick_rider, TripID, JavaStatus, Candidates: ...}`
   - Else load DE by `trip.DEPhone`. If DE is `busy` AND `de.CurrentOrderID != ""` AND `de.CurrentOrderID != trip.OrderID` → `{Mode: blocked, Reason: "rider_busy_elsewhere", TripID, Rider, JavaStatus}`
   - Else → `{Mode: force_progress, TripID, Rider, JavaStatus}`
6. Candidates: page `ListByAssignedStore(ctx, models.AssignedStoreIndexKeyFor(trip.StoreID), "", cursor, 100)` until no cursor. Keep status `offline`/`eligible`/`free`. Sort by name already from GSI. Do not include busy.

- [ ] **Step 1: Write the failing tests** (append to `trip_service_test.go`)

Add `stubJavaOrder` and expand stubs first *in the test file only*, then tests that call `PreviewAdminDropByOrder` (which does not exist yet).

```go
type stubJavaOrder struct {
	status    string
	getErr    error
	updates   []string
	updateErr error
}

func (s *stubJavaOrder) GetOrderStatus(_ context.Context, _ string) (string, error) {
	if s.getErr != nil {
		return "", s.getErr
	}
	return s.status, nil
}

func (s *stubJavaOrder) UpdateOrderStatus(_ context.Context, _, status, _ string) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	s.updates = append(s.updates, status)
	return nil
}
```

Expand `stubDERepo`:

```go
type stubDERepo struct {
	de          *models.DeliveryExecutive
	byPhone     map[string]*models.DeliveryExecutive
	listed      []*models.DeliveryExecutive
	statusCalls []string
	attachCalls int
}

func (s *stubDERepo) GetByPhone(_ context.Context, phone string) (*models.DeliveryExecutive, error) {
	if s.byPhone != nil {
		return s.byPhone[phone], nil
	}
	if s.de != nil && (phone == "" || phone == s.de.PhoneNumber) {
		return s.de, nil
	}
	return s.de, nil
}

func (s *stubDERepo) UpdateStatus(_ context.Context, phone string, status models.DEStatus, _, _ string) error {
	s.statusCalls = append(s.statusCalls, phone+":"+string(status))
	if de, err := s.GetByPhone(context.Background(), phone); err == nil && de != nil {
		de.Status = status
	}
	return nil
}

func (s *stubDERepo) AttachToTrip(_ context.Context, phone, orderID, tripID, _ string) error {
	s.attachCalls++
	if de, err := s.GetByPhone(context.Background(), phone); err == nil && de != nil {
		de.Status = models.DEStatusBusy
		de.CurrentOrderID = orderID
		de.CurrentTripID = tripID
	}
	return nil
}

func (s *stubDERepo) ListByAssignedStore(_ context.Context, _, _, _ string, _ int32) ([]*models.DeliveryExecutive, string, error) {
	return s.listed, "", nil
}
```

On `stubTripRepo` add:

```go
adminAssignCalled bool

func (s *stubTripRepo) AdminAssign(_ context.Context, _, _, deID, dePhone, _ string) error {
	s.adminAssignCalled = true
	if s.trip != nil {
		s.trip.Status = models.TripStatusAccepted
		s.trip.DEID = deID
		s.trip.DEPhone = dePhone
	}
	return nil
}

func (s *stubTripRepo) Accept(_ context.Context, _, _ string) error {
	if s.trip != nil {
		s.trip.Status = models.TripStatusAccepted
	}
	return nil
}
```

Tests (exact names):

```go
func TestPreviewAdminDropByOrder_NoTrip_JavaReady(t *testing.T) {
	svc := newTripServiceForTest(&stubTripRepo{}, &stubDERepo{}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	p, err := svc.PreviewAdminDropByOrder(context.Background(), "ORD-P1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != AdminDropModeJavaOnly {
		t.Fatalf("mode=%s", p.Mode)
	}
}

func TestPreviewAdminDropByOrder_NoTrip_JavaPacking_Blocked(t *testing.T) {
	svc := newTripServiceForTest(&stubTripRepo{}, &stubDERepo{}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "PACKING"}
	p, err := svc.PreviewAdminDropByOrder(context.Background(), "ORD-P2")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != AdminDropModeBlocked || p.Reason != "java_not_ready" {
		t.Fatalf("got %+v", p)
	}
}

func TestPreviewAdminDropByOrder_JavaCancelled_Blocked(t *testing.T) {
	svc := newTripServiceForTest(&stubTripRepo{trip: &models.Trip{TripID: "t1", Status: models.TripStatusCreated}}, &stubDERepo{}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "CANCELLED"}
	p, err := svc.PreviewAdminDropByOrder(context.Background(), "ORD-P3")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != AdminDropModeBlocked || p.Reason != "java_cancelled" {
		t.Fatalf("got %+v", p)
	}
}

func TestPreviewAdminDropByOrder_UnassignedTrip_PickRider(t *testing.T) {
	listed := []*models.DeliveryExecutive{
		{PhoneNumber: "+2601", Name: "Ann", Status: models.DEStatusOffline, InHandCashZMW: 10, AssignedStoreID: "221"},
		{PhoneNumber: "+2602", Name: "Bob", Status: models.DEStatusBusy, InHandCashZMW: 1, AssignedStoreID: "221"},
		{PhoneNumber: "+2603", Name: "Cyd", Status: models.DEStatusFree, InHandCashZMW: 99, AssignedStoreID: "221"},
	}
	svc := newTripServiceForTest(&stubTripRepo{trip: &models.Trip{
		TripID: "t1", OrderID: "ORD-P4", StoreID: "221", Status: models.TripStatusCreated,
	}}, &stubDERepo{listed: listed}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	p, err := svc.PreviewAdminDropByOrder(context.Background(), "ORD-P4")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != AdminDropModePickRider {
		t.Fatalf("mode=%s", p.Mode)
	}
	if len(p.Candidates) != 2 {
		t.Fatalf("candidates=%d (busy must be excluded)", len(p.Candidates))
	}
}

func TestPreviewAdminDropByOrder_AssignedRider_ForceProgress(t *testing.T) {
	de := &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+2609", Name: "Ghan", Status: models.DEStatusOffline}
	svc := newTripServiceForTest(&stubTripRepo{trip: &models.Trip{
		TripID: "t1", OrderID: "ORD-P5", DEPhone: "+2609", DEID: "de-1", Status: models.TripStatusAccepted,
	}}, &stubDERepo{de: de}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	p, err := svc.PreviewAdminDropByOrder(context.Background(), "ORD-P5")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != AdminDropModeForceProgress || p.Rider == nil || p.Rider.Phone != "+2609" {
		t.Fatalf("got %+v", p)
	}
}

func TestPreviewAdminDropByOrder_RiderBusyElsewhere_Blocked(t *testing.T) {
	de := &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+2609", Status: models.DEStatusBusy, CurrentOrderID: "ORD-OTHER"}
	svc := newTripServiceForTest(&stubTripRepo{trip: &models.Trip{
		TripID: "t1", OrderID: "ORD-P6", DEPhone: "+2609", DEID: "de-1", Status: models.TripStatusAssigned,
	}}, &stubDERepo{de: de}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	p, err := svc.PreviewAdminDropByOrder(context.Background(), "ORD-P6")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != AdminDropModeBlocked || p.Reason != "rider_busy_elsewhere" {
		t.Fatalf("got %+v", p)
	}
}

func TestPreviewAdminDropByOrder_AlreadyDone(t *testing.T) {
	svc := newTripServiceForTest(&stubTripRepo{trip: &models.Trip{
		TripID: "t1", OrderID: "ORD-P7", Status: models.TripStatusCompleted,
	}}, &stubDERepo{}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "DELIVERED"}
	p, err := svc.PreviewAdminDropByOrder(context.Background(), "ORD-P7")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != AdminDropModeAlreadyDone {
		t.Fatalf("mode=%s", p.Mode)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/service -count=1 -run 'TestPreviewAdminDropByOrder' 
```

Expected: FAIL compile (`PreviewAdminDropByOrder` undefined) or FAIL missing type.

- [ ] **Step 3: Minimal implementation**

Implement types, sentinels, `javaOrderAPI`, interface expansions, stub methods on production interfaces, and `PreviewAdminDropByOrder` per the rules above. Do **not** change `AdminCompleteDropByOrder` behavior in this task.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/service -count=1 -run 'TestPreviewAdminDropByOrder|TestAdminCompleteDropByOrder|TestAdminCompleteTask'
```

Expected: PASS. Existing admin-drop tests still pass.

- [ ] **Step 5: Commit**

```bash
git add internal/service/trip_service.go internal/service/trip_service_test.go
git commit -m "$(cat <<'EOF'
feat: add admin drop preview modes for force-deliver

EOF
)"
```

### Task 2: Force-deliver `AdminCompleteDropByOrder`

**Files:**
- Modify: `internal/service/trip_service.go` (`AdminCompleteDropByOrder` signature + body)
- Modify: `internal/repository/de_repository.go` (`AttachToTrip`)
- Modify: `internal/service/trip_service_test.go`
- Modify: every production caller of `AdminCompleteDropByOrder` to pass `driverPhone` (handler still Task 3 — add the param now; handler Task 3 wires it). Until Task 3, the handler will not compile if left on the old signature — **update the handler call in this task to pass `""`** so `go test ./...` stays green: `AdminCompleteDropByOrder(ctx, orderID, adminUsername, "")`.
- Test: `go test ./internal/service -count=1 -run 'TestAdminCompleteDropByOrder|TestPreviewAdminDropByOrder'`

**Interfaces:**
- Consumes: `PreviewAdminDropByOrder`, `AdminAssign`, `Accept`, `AttachToTrip`, `UpdateStatus`, `applyTaskCompletion`, `javaOrderAPI.UpdateOrderStatus` / `GetOrderStatus`
- Produces: `func (s *TripService) AdminCompleteDropByOrder(ctx context.Context, orderID, adminUsername, driverPhone string) error`

Signature change:

```go
func (s *TripService) AdminCompleteDropByOrder(ctx context.Context, orderID, adminUsername, driverPhone string) error
```

Algorithm:

1. `preview, err := s.PreviewAdminDropByOrder(ctx, orderID)`
2. Switch `preview.Mode`:
   - `blocked` + `java_cancelled` → `ErrJavaOrderCancelled`
   - `blocked` + `java_not_ready` → `ErrOrderNotDeliverable`
   - `blocked` + `rider_busy_elsewhere` → `ErrRiderBusyElsewhere`
   - `already_done` → `ErrAlreadyDelivered`
   - `java_only` → `forceJavaDeliver(ctx, orderID, adminUsername)` (see below)
   - `pick_rider` → if `strings.TrimSpace(driverPhone)==""` → `ErrRiderRequired`. Else `forceAssignAndComplete(...)`
   - `force_progress` → `forceProgressExisting(...)` (ignore `driverPhone`)

`forceJavaDeliver`:
- `st, _ := java.GetOrderStatus`
- If `st == "DELIVERED"` return nil
- If `st == "READY_FOR_DELIVERY"` call `UpdateOrderStatus(orderID, "OUT_FOR_DELIVERY", "ADMIN:"+adminUsername)` then `UpdateOrderStatus(..., "DELIVERED", ...)`
- If `st == "OUT_FOR_DELIVERY"` call `UpdateOrderStatus(..., "DELIVERED", ...)`
- Else `ErrOrderNotDeliverable`
- These Java writes are **synchronous** (not `syncJavaWithRetry` goroutine) so the POST fails if Java refuses (active pick task included).

`forceAssignAndComplete`:
- `de := GetByPhone(driverPhone)`; nil → `ErrTripNotFound` is wrong — use `fmt.Errorf("%w: %s", admin. wait)` — use existing `ErrDENotFound` from `admin_service.go` (`service.ErrDENotFound`) or add `ErrDENotFound` to trip_service if it does not exist. Prefer `admin_service.go`'s `ErrDENotFound` — it is already `var ErrDENotFound` in package `service`.
- If `de.Status == busy` → `ErrRiderBusyElsewhere` (cannot pick a busy rider)
- `prior := de.Status`
- If `de.Status != eligible` → `UpdateStatus(phone, eligible, trip.StoreID, "")`
- `tripRepo.AdminAssign(trip.TripID, trip.OrderID, de.DEID, de.PhoneNumber, trip.StoreID)`
- Reload trip (mutate stub in tests; in prod GetByOrderID again)
- `completePickupThenDrop(ctx, trip, de, adminUsername)`
- If `prior == offline` → `UpdateStatus(phone, offline, "", "")`

`forceProgressExisting`:
- Load trip + DE from `trip.DEPhone`
- If busy elsewhere (same check as preview) → `ErrRiderBusyElsewhere`
- `prior := de.Status`
- If `de.CurrentOrderID != trip.OrderID` OR `de.Status != busy` → `AttachToTrip(de.PhoneNumber, trip.OrderID, trip.TripID, trip.StoreID)`
- If `trip.Status == assigned` → `tripRepo.Accept` then set in-memory `trip.Status = accepted`
- `completePickupThenDrop`
- If `prior == offline` → `UpdateStatus(offline, "", "")`

`completePickupThenDrop` (unexported helper):
- If pickup task not completed:
  - If trip status is not `accepted`, Accept first (covers `created` should not happen here)
  - Select pickup task, `validateTaskTransition` to completed (requireReached false), **do not** call `validateTaskAgainstTripStatus` after you have set accepted — or set `trip.Status = accepted` then call existing `applyTaskCompletion` for pickup
- Reload/update in-memory trip status to `out_for_delivery` after pickup (`onTaskCompleted` already calls `UpdateStatus` to OFD; also set `trip.Status = OutForDelivery` in memory)
- If Java is already `DELIVERED`, skip `syncJavaWithRetry` for OFD/DELIVERED — pass a flag or check status before `applyTaskCompletion`'s java sync. **Minimal approach:** if `preview.JavaStatus == "DELIVERED"`, still run trip completion (COD/payout) but do not call Java. Implement by wrapping: if java already DELIVERED, temporarily skip by checking inside a new helper `maybeSyncJava` used only from this force path's drop/pickup — **do not** change rider `applyTaskCompletion` for the driver app.
  - Cleanest: `completePickupThenDrop` calls `applyTaskCompletion` as today (async Java). Extra Java OFD/DELIVERED on an already-DELIVERED order may 400 and get retried — acceptable **only if** Java rejects no-op. Safer: before `applyTaskCompletion` for this force path, if Java is already DELIVERED, set `s.javaClient` skip... **Don't.** Instead add parameter `skipJava bool` to a private `applyTaskCompletionForce` copy? YAGNI: add `adminSkipJava bool` field on a small options struct.
  - **Required behavior:** if `preview.JavaStatus == "DELIVERED"`, do not call `UpdateOrderStatus`. Implement `applyTaskCompletion` with an extra `skipJava bool` **only used from the force-deliver helpers**. Rider path keeps `skipJava=false`.

`AttachToTrip` in `de_repository.go` (real Dynamo):

```go
func (r *DERepository) AttachToTrip(ctx context.Context, phone, orderID, tripID, storeID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression: aws.String("SET #s = :busy, current_order_id = :oid, current_trip_id = :tid, current_store_id = :store, updated_at = :now REMOVE duty_index_key, scan_deadline_at"),
		ExpressionAttributeNames: map[string]string{"#s": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":busy":  &types.AttributeValueMemberS{Value: string(models.DEStatusBusy)},
			":oid":   &types.AttributeValueMemberS{Value: orderID},
			":tid":   &types.AttributeValueMemberS{Value: tripID},
			":store": &types.AttributeValueMemberS{Value: storeID},
			":now":   &types.AttributeValueMemberS{Value: now},
		},
	})
	return err
}
```

`*repository.DERepository` already used as `deRepoI` — adding methods to the interface requires them on the real repo. `UpdateStatus` and `ListByAssignedStore` already exist. `AdminAssign` already exists on `TripRepository`.

**Existing test change (required):** `TestAdminCompleteDropByOrder_RequiresOutForDelivery` currently expects `ErrPrerequisiteIncomplete`. Change it to expect **success** (force pickup then drop) and assert `completeTripCalled` / drop completed. Update its `AdminCompleteDropByOrder` call to pass `""` as driverPhone. Keep `TestAdminCompleteTask_Drop_RequiresOutForDelivery` failing on pickup-not-done (phone-scoped).

`TestAdminCompleteDropByOrder_NoTrip_NotFound`: change to java_only success when stub Java is READY — or expect `ErrOrderNotDeliverable` if java stub is nil/empty. Set `svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}` and assert `updates` is `["OUT_FOR_DELIVERY","DELIVERED"]`.

- [ ] **Step 1: Write failing tests**

```go
func TestAdminCompleteDropByOrder_NoTrip_JavaOnly(t *testing.T) {
	java := &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	svc := newTripServiceForTest(&stubTripRepo{}, &stubDERepo{}, &stubNotifier{})
	svc.javaClient = java
	if err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-J1", "ops", ""); err != nil {
		t.Fatal(err)
	}
	if len(java.updates) != 2 || java.updates[0] != "OUT_FOR_DELIVERY" || java.updates[1] != "DELIVERED" {
		t.Fatalf("updates=%v", java.updates)
	}
}

func TestAdminCompleteDropByOrder_ForceProgress_PickupThenDrop(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "t5", OrderID: "ORD-ADMIN-5", StoreID: "221",
		DEID: "de-1", DEPhone: "+260971000005",
		Status: models.TripStatusAccepted,
		Tasks: []models.Task{
			{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
			{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
		},
	}}
	de := &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000005", Status: models.DEStatusOffline}
	deRepo := &stubDERepo{de: de}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	if err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-ADMIN-5", "ops", ""); err != nil {
		t.Fatal(err)
	}
	if !repo.completeTripCalled {
		t.Fatal("expected drop to complete the trip")
	}
	if got := deRepo.statusCalls[len(deRepo.statusCalls)-1]; got != "+260971000005:offline" {
		t.Fatalf("expected restore offline, last call %q", got)
	}
}

func TestAdminCompleteDropByOrder_PickRider_RequiresPhone(t *testing.T) {
	svc := newTripServiceForTest(&stubTripRepo{trip: &models.Trip{
		TripID: "t1", OrderID: "ORD-U1", StoreID: "221", Status: models.TripStatusCreated,
	}}, &stubDERepo{}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-U1", "ops", "")
	if !errors.Is(err, ErrRiderRequired) {
		t.Fatalf("got %v", err)
	}
}

func TestAdminCompleteDropByOrder_PickRider_AssignsAndCompletes(t *testing.T) {
	de := &models.DeliveryExecutive{DEID: "de-9", PhoneNumber: "+260770990570", Status: models.DEStatusOffline, AssignedStoreID: "221"}
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "t1", OrderID: "ORD-U2", StoreID: "221", Status: models.TripStatusCreated,
		Tasks: []models.Task{
			{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
			{TaskID: "d", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
		},
	}}
	deRepo := &stubDERepo{de: de}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	if err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-U2", "ops", "+260770990570"); err != nil {
		t.Fatal(err)
	}
	if !repo.adminAssignCalled || !repo.completeTripCalled {
		t.Fatal("expected assign + complete")
	}
	if got := deRepo.statusCalls[len(deRepo.statusCalls)-1]; got != "+260770990570:offline" {
		t.Fatalf("restore offline, got %q", got)
	}
}

func TestAdminCompleteDropByOrder_BusyElsewhere(t *testing.T) {
	de := &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+2609", Status: models.DEStatusBusy, CurrentOrderID: "ORD-OTHER"}
	svc := newTripServiceForTest(&stubTripRepo{trip: &models.Trip{
		TripID: "t1", OrderID: "ORD-B1", DEPhone: "+2609", DEID: "de-1", Status: models.TripStatusAssigned,
	}}, &stubDERepo{de: de}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-B1", "ops", "")
	if !errors.Is(err, ErrRiderBusyElsewhere) {
		t.Fatalf("got %v", err)
	}
}
```

Rewrite `TestAdminCompleteDropByOrder_RequiresOutForDelivery` as an alias of force-progress success **or delete it** and rely on `TestAdminCompleteDropByOrder_ForceProgress_PickupThenDrop` (same order id). Prefer delete the old test to avoid duplication.

Rewrite `TestAdminCompleteDropByOrder_NoTrip_NotFound` into the java_only test above (delete old name).

Keep `TestAdminCompleteDropByOrder_FindsTripAndCompletes` working with the new 4-arg signature and a java stub (`OUT_FOR_DELIVERY` or `READY_FOR_DELIVERY`). Keep `TestAdminCompleteDropByOrder_TerminalTrip_Rejected` — if trip completed and java DELIVERED, expect `ErrAlreadyDelivered`; if trip completed and java READY, `java_only` should sync Java (not `ErrTripClosed`). **Change this test:** set java `DELIVERED` and expect `ErrAlreadyDelivered`.

- [ ] **Step 2: Run tests — expect FAIL**

```bash
go test ./internal/service -count=1 -run 'TestAdminCompleteDropByOrder'
```

- [ ] **Step 3: Implement** `AttachToTrip`, new `AdminCompleteDropByOrder`, helpers. Update all existing `AdminCompleteDropByOrder(` call sites in this package's tests to the 4-arg form.

- [ ] **Step 4: Run tests — expect PASS**

```bash
go test ./internal/service ./internal/handlers ./internal/repository -count=1
```

- [ ] **Step 5: Commit**

```bash
git add internal/service/trip_service.go internal/service/trip_service_test.go internal/repository/de_repository.go internal/handlers/admin_driver_handlers.go
git commit -m "$(cat <<'EOF'
feat: force-deliver admin order drop (assign, pickup, java-only)

EOF
)"
```

### Task 3: HTTP preview + POST body

**Files:**
- Modify: `internal/handlers/admin_driver_handlers.go`
- Modify: `internal/handlers/trip_handlers.go` (`classifyTaskUpdateError`)
- Modify: `cmd/server/main.go`
- Modify: `internal/handlers/trip_handlers_test.go` (add classify cases)
- Test: `go test ./internal/handlers -count=1 -run 'TestClassifyTaskUpdateError|TestAdmin'`

**Interfaces:**
- Consumes: `PreviewAdminDropByOrder`, `AdminCompleteDropByOrder(..., driverPhone)`
- Produces:
  - `GET /api/v1/admin/orders/{orderId}/drop/preview` → 200 `AdminDropPreview` JSON
  - POST complete reads optional JSON `{ "driver_phone": "..." }` (empty body still OK)

Handler preview:

```go
func (h *AdminDriverHandlers) PreviewAdminDrop(w http.ResponseWriter, r *http.Request) {
	orderID := strings.TrimSpace(mux.Vars(r)["orderId"])
	if orderID == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "orderId is required")
		return
	}
	preview, err := h.tripService.PreviewAdminDropByOrder(r.Context(), orderID)
	if err != nil {
		h.logger.WithError(err).Error("admin: drop preview failed")
		h.respondWithError(w, http.StatusInternalServerError, "PREVIEW_FAILED", "Failed to preview drop")
		return
	}
	h.respondWithJSON(w, http.StatusOK, preview)
}
```

POST: decode optional body; ignore decode error on empty body; pass `req.DriverPhone` into `AdminCompleteDropByOrder`.

`classifyTaskUpdateError` additions:

| error | status | code |
|---|---|---|
| `ErrOrderNotDeliverable` | 409 | `ORDER_NOT_DELIVERABLE` |
| `ErrJavaOrderCancelled` | 409 | `ORDER_CANCELLED` |
| `ErrRiderRequired` | 400 | `RIDER_REQUIRED` |
| `ErrRiderBusyElsewhere` | 409 | `RIDER_BUSY_ELSEWHERE` |
| `ErrAlreadyDelivered` | 409 | `ALREADY_DELIVERED` |
| `ErrDENotFound` | 404 | `DRIVER_NOT_FOUND` |

Route (next to existing complete):

```go
admin.HandleFunc("/orders/{orderId}/drop/preview", adminDriverHandlers.PreviewAdminDrop).Methods("GET", "OPTIONS")
```

- [ ] **Step 1: Write failing classify tests** in `trip_handlers_test.go` (follow the existing table-driven cases).

- [ ] **Step 2: Run — expect FAIL** (`errors.Is` not classified → 500).

- [ ] **Step 3: Implement classify + handlers + route.**

- [ ] **Step 4:**

```bash
go test ./internal/handlers ./internal/service ./cmd/server -count=1
```

- [ ] **Step 5: Commit**

```bash
git commit -m "$(cat <<'EOF'
feat: admin drop preview HTTP and force-deliver error codes

EOF
)"
```

### Task 4: Admin dashboard modal

**Repo:** `/Users/shivangawasthi/bunzo/admin-dashboard`  
**Branch:** `feat/admin-force-deliver` (create if needed; do not commit to `main`)

**Files:**
- Modify: `src/lib/types.ts`, `src/lib/api.ts`, `src/app/orders/[orderNumber]/page.tsx`
- Create: `src/lib/adminDropPreview.ts`, `src/lib/adminDropPreview.test.mjs`

**Interfaces:**
- Consumes: GET `/admin/orders/{orderNumber}/drop/preview`, POST `/admin/orders/{orderNumber}/drop/complete` with optional `{ driver_phone }`
- Produces: modal UI per mode

```ts
// src/lib/adminDropPreview.ts
export function canConfirmAdminDrop(mode, selectedPhone) {
  if (mode === 'blocked' || mode === 'already_done') return false;
  if (mode === 'pick_rider') return Boolean(selectedPhone);
  return mode === 'java_only' || mode === 'force_progress';
}

export function adminDropConfirmLabel(mode) {
  switch (mode) {
    case 'java_only': return 'Mark delivered (no trip)';
    case 'pick_rider': return 'Assign and mark delivered';
    case 'force_progress': return 'Mark delivered';
    default: return 'Confirm';
  }
}
```

`node --test src/lib/adminDropPreview.test.mjs` asserts:
- `canConfirmAdminDrop('pick_rider', '') === false`
- `canConfirmAdminDrop('pick_rider', '+2601') === true`
- `canConfirmAdminDrop('java_only', '') === true`
- `canConfirmAdminDrop('blocked', '+2601') === false`

API:

```ts
getAdminDropPreview: (orderNumber: string) =>
  request<AdminDropPreview>(`/admin/orders/${encodeURIComponent(orderNumber.trim())}/drop/preview`),
adminCompleteOrderDrop: (orderNumber: string, driverPhone?: string) =>
  request<{ status: string }>(`/admin/orders/${encodeURIComponent(orderNumber.trim())}/drop/complete`, {
    method: 'POST',
    body: driverPhone ? { driver_phone: driverPhone } : {}
  }),
```

Order page: when `statusTarget` becomes `DELIVERED`, fetch preview. Show spinner while loading. Render:
- `blocked`: red `ErrorBox` with human text (`java_cancelled` → “This order is cancelled.”; `java_not_ready` → “Order is not ready for delivery yet.”; `rider_busy_elsewhere` → “Assigned rider is on another trip. Reassign this order first.”). Hide Confirm.
- `already_done`: “Already delivered.” Hide Confirm.
- `java_only`: “No delivery trip exists. This will mark the order delivered in order-service only (no rider COD/payout).”
- `force_progress`: “Mark delivered for {rider.name} ({rider.phone})? This will complete pickup if needed, then the drop.”
- `pick_rider`: `<select>` of candidates (`{name} — {phone} — {status} — K{cash}`) + Continue using `adminDropConfirmLabel`.

On Confirm: `adminCompleteOrderDrop(orderNumber, selectedPhone or undefined)`.

- [ ] **Step 1: Write `adminDropPreview.test.mjs` first, run `node --test src/lib/adminDropPreview.test.mjs` — FAIL (module missing).**
- [ ] **Step 2: Implement helper — test PASS.**
- [ ] **Step 3: Wire types, api, page.**
- [ ] **Step 4: `npx tsc --noEmit` and `node --test src/lib/adminDropPreview.test.mjs`.**
- [ ] **Step 5: Commit on `feat/admin-force-deliver` in admin-dashboard.**

```bash
git commit -m "$(cat <<'EOF'
feat: force-deliver confirm modal driven by drop preview

EOF
)"
```

---

## Self-review

**Spec coverage:**
- No trip → java_only / blocked — Task 1+2
- Unassigned trip → dropdown + assign — Task 1+2+4
- Assigned rider → force_progress — Task 1+2
- Busy elsewhere — Task 1+2
- Already DELIVERED Java + open trip — skip Java, close trip — Task 2
- Cancelled Java — blocked — Task 1+2
- Restore offline — Task 2
- Candidates exclude busy, include cash — Task 1+4
- Same POST URL — Task 3
- Preview GET — Task 1+3+4
- Phone-scoped drop unchanged — Task 2 keeps `AdminCompleteTask` gate
- No trip created — Task 2 java_only
- Over-limit allowed — no cash check in pick_rider

**Placeholder scan:** none.

**Type consistency:** `AdminDropMode` / `driver_phone` / preview JSON tags used in Tasks 1–4.
