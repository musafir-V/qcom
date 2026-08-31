# Early trip assignment + packed pickup gate

> **REQUIRED SUB-SKILL:** superpowers:subagent-driven-development. Dispatch independent tasks in parallel waves (Wave 1: Task 1 || Task 2 || Task 3 || Task 4; Wave 2: Task 5 after T3+T4). TDD iron law: no production code without a failing test first.

**Goal:** Create last-mile trips as soon as Java has CONFIRMED/PACKING/READY_FOR_DELIVERY, FIFO-assign when a rider is free, block rider pickup/verify-pickup until Java is READY_FOR_DELIVERY, stamp ordered quantity at create, accept Java packed snapshot on POST /internal/v1/trips/edit-by-order.

**Architecture:** The assignment cron already calls `GetReadyForDeliveryOrders` then FIFO-creates/assigns trips. Widen the Java poll to one GET `/order-service/api/v1/orders/store/{id}/by-statuses` with repeated `statuses=CONFIRMED&statuses=PACKING&statuses=READY_FOR_DELIVERY`. Keep `eligibleOrderStatus = "READY_FOR_DELIVERY"` as the **pickup gate** constant. Poll statuses live in a **separate** `assignableOrderStatuses` list in `java_order_client.go` — do not reuse the pickup const as the poll filter. Rider `VerifyPickup` and rider `UpdateTaskStatus` pickup-complete call `requirePacked` (`GetOrderStatus` vs `eligibleOrderStatus`). AdminCompleteTask / skipJava / `completePickupThenDrop` stay ungated. Trip create stamps `createQuantity` (ordered → legacy → 0). Java packed snapshot lands on the existing internal no-key router via `UpdateEditByOrder` (copy of `UpdatePayment` condition).

**Tech Stack:** Go 1.25, gorilla/mux, DynamoDB, stdlib testing + httptest. No testify. qcom-only.

## Global Constraints

- TDD iron law: write the failing test first, watch it fail for the right reason, then implement. ≥85% new-code coverage.
- Never clone. Never invent APIs. Follow existing names, stubs, sentinels, and HTTP shapes already in this repo.
- qcom-only. Do not touch `payout_service`, `eta_service`, AcceptTrip **behavior**, `detectCancellations`, admin-dashboard, inventory.
- Do **NOT** delete `eligibleOrderStatus`. Keep it `READY_FOR_DELIVERY` for the pickup gate (`requirePacked`). Poll statuses are a **SEPARATE** list in `java_order_client.go` (`CONFIRMED`, `PACKING`, `READY_FOR_DELIVERY`). Do not reuse the pickup const as the poll filter.
- Leave `EffectiveQuantity()` fulfilled-first. Do **NOT** invert `TestJavaOrderItem_EffectiveQuantityPrefersFulfilled` or `TestJavaOrder_DecodesItemsAndDeliveryName`.
- Rider path **ONLY** for packed gate: `VerifyPickup` + `UpdateTaskStatus` when completing pickup. `AdminCompleteTask` / skipJava / `completePickupThenDrop` stay **UNGATED**.
- Existing pickup-complete tests that have nil `javaClient` must be wired with `stubJavaOrder{status:"READY_FOR_DELIVERY"}` so they still pass.
- Internal `/internal/v1` still has **no API key**. No `ListOrders`. No `UpdateItems` today — T4 new repo method `UpdateEditByOrder` is right. No `/trips/items/update`.
- Do not move `syncJavaWithRetry` `OUT_FOR_DELIVERY`.
- Keep `GetReadyForDeliveryOrders` name. Do not modify `assignment_cron.go` in T1.
- FIFO / create / assign logic unchanged.
- `trip_handlers_test.go` has no payment/update httptest today (classify tables only). T5 copies `TestUpdateTripPayment_*` **service** tests; do not invent a full httptest suite payment/update lacks. A missing-`order_id` → 400 handler test is OK.
- If `UpdatePayment` already has a fake-client test, add a sibling; else do not invent a Dynamo fake.
- Do not commit secrets, binaries, or unrelated untracked files. `*.md` is gitignored: only the plan file is force-added. Do not commit other markdown.

## File Map

**Modify**
- `internal/service/java_order_client.go` + `java_order_client_test.go` (T1)
- `internal/service/assignment_cron.go` + `assignment_cron_test.go` (T2)
- `internal/service/trip_service.go` + `trip_service_test.go` (T3, T5)
- `internal/handlers/trip_handlers.go` + `trip_handlers_test.go` (T3, T5)
- `internal/repository/trip_repository.go` (T4)
- `cmd/server/main.go` (T5 route only)

**Do not create** `/trips/items/update`, `ListOrders`, or a second items HTTP path.

## Waves

- **Wave 1 (parallel, independent files):** Task 1 || Task 2 || Task 3 || Task 4
- **Wave 2 (after T3+T4):** Task 5

---

## Task 1: Java GET by-statuses

**Files:**
- Modify: `internal/service/java_order_client.go`
- Modify: `internal/service/java_order_client_test.go`
- Do **not** modify `assignment_cron.go` in this task.

**Existing contract (do not invent):**
- `GetReadyForDeliveryOrders(ctx, storeID)` keeps its name.
- Today it GETs `{base}{orderServicePathPrefix}/api/v1/orders/store/{storeID}?status={eligibleOrderStatus}&pageNum=&pageSize=50` and paginates while `meta.last` is false.
- `orderServicePathPrefix = "/order-service"`.
- `eligibleOrderStatus` lives in `assignment_cron.go` and **stays** there (`READY_FOR_DELIVERY`). After this task the client must **not** use it as the poll filter.

- [ ] **Step 1: RED — write `TestGetReadyForDeliveryOrders_UsesByStatusesRepeatedQuery`**

In `java_order_client_test.go`, httptest server that records every request:

- Exactly **1 GET** (page 0, `meta.last=true`).
- Path: `/order-service/api/v1/orders/store/100/by-statuses`
- **NO** `status=` query key.
- `statuses` repeated: `CONFIRMED`, `PACKING`, `READY_FOR_DELIVERY` (use `r.URL.Query()["statuses"]`).
- `pageNum=0`, `pageSize=50`.
- Return a one-order `content`/`meta` body so the existing decode/stamp path still runs.

Also update `TestGetReadyForDeliveryOrders_StampsStoreIDAndToleratesNumericStoreId` expected path from `/order-service/api/v1/orders/store/100` to `/order-service/api/v1/orders/store/100/by-statuses`.

```bash
go test -v ./internal/service/ -run 'GetReadyForDeliveryOrders|GetOrderRaw'
```

Expected: new test FAIL (path still `.../store/100` with `status=`). The stamps test FAIL on path. `GetOrderRaw` tests still PASS.

- [ ] **Step 2: GREEN — implement one by-statuses GET**

In `java_order_client.go`:

```go
var assignableOrderStatuses = []string{"CONFIRMED", "PACKING", "READY_FOR_DELIVERY"}
```

Build the URL with `net/url`: path `/api/v1/orders/store/{storeID}/by-statuses`, then `q.Add("statuses", st)` for each entry in `assignableOrderStatuses`, plus `pageNum` and `pageSize`. **One GET per page**, not three GETs. Keep the existing pagination loop, decode, `normalizeOrderID`, and StoreID stamp.

Do **not** move or delete `eligibleOrderStatus`.

- [ ] **Step 3: Verify**

```bash
go test -v ./internal/service/ -run 'GetReadyForDeliveryOrders|GetOrderRaw'
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/service/java_order_client.go internal/service/java_order_client_test.go
git commit -m "feat: poll CONFIRMED/PACKING/RFD in one by-statuses GET"
```

---

## Task 2: Stamp ordered quantity at create

**Files:**
- Modify: `internal/service/assignment_cron.go`
- Modify: `internal/service/assignment_cron_test.go`

**Existing contract (do not invent):**
- `tripItemsFromOrder` maps `ProductName`/`ImageURL`/`Sku` and today uses `it.EffectiveQuantity()`.
- `EffectiveQuantity()` stays fulfilled-first: fulfilled → ordered → legacy → 0. Do **not** change it.
- `eligibleOrderStatus` stays defined in this file. Do **not** delete it. Do **not** touch `detectCancellations`.
- FIFO / create / assign logic unchanged.

- [ ] **Step 1: RED — invert MapsFields + add two tests**

Invert `TestTripItemsFromOrder_MapsFields`: Milk quantity **2 → 3** (ordered 3, fulfilled 2). Bread stays 1.

Add:

```go
func TestTripItemsFromOrder_IgnoresFulfilledZeroAtCreate(t *testing.T) {
	// ordered 3, fulfilled 0 → 3 at create (ignore fulfilled)
}

func TestTripItemsFromOrder_LegacyQuantityWhenOrderedAbsent(t *testing.T) {
	// legacy quantity only → that value
}
```

Do **NOT** invert `TestJavaOrderItem_EffectiveQuantityPrefersFulfilled` or `TestJavaOrder_DecodesItemsAndDeliveryName`.

```bash
go test -v ./internal/service/ -run 'TripItemsFromOrder|buildTripFromOrder|AssignmentCron'
```

Expected: `TestTripItemsFromOrder_MapsFields` FAIL (got 2, want 3). New tests FAIL (got 0 / fulfilled). Confirm the two EffectiveQuantity tests still **PASS**:

```bash
go test -v ./internal/service/ -run 'TestJavaOrderItem_EffectiveQuantityPrefersFulfilled|TestJavaOrder_DecodesItemsAndDeliveryName'
```

- [ ] **Step 2: GREEN — `createQuantity`**

```go
func createQuantity(it JavaOrderItem) int {
	if it.OrderedQuantity != nil {
		return *it.OrderedQuantity
	}
	if it.LegacyQuantity != nil {
		return *it.LegacyQuantity
	}
	return 0
}
```

`tripItemsFromOrder` uses `createQuantity(it)` for `Quantity`. Do **not** change `EffectiveQuantity`.

Update `processStore` comments/logs from “READY_FOR_DELIVERY orders” to CONFIRMED/PACKING/READY_FOR_DELIVERY (fetch path only). Leave `detectCancellations` comments and code untouched.

- [ ] **Step 3: Verify**

```bash
go test -v ./internal/service/ -run 'TripItemsFromOrder|buildTripFromOrder|AssignmentCron'
go test -v ./internal/service/ -run 'TestJavaOrderItem_EffectiveQuantityPrefersFulfilled|TestJavaOrder_DecodesItemsAndDeliveryName'
```

Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/service/assignment_cron.go internal/service/assignment_cron_test.go
git commit -m "fix: stamp ordered quantity on trip create, ignore fulfilled"
```

---

## Task 3: Rider packed gate

**Files:**
- Modify: `internal/service/trip_service.go`
- Modify: `internal/service/trip_service_test.go`
- Modify: `internal/handlers/trip_handlers.go`
- Modify: `internal/handlers/trip_handlers_test.go`

**Existing contract (do not invent):**
- Sentinel errors live in the `var (` block at the top of `trip_service.go`.
- `VerifyPickup` → `ownedTrip` → `validatePickupScan` → nil. Gate **after** `validatePickupScan`.
- `UpdateTaskStatus` rider path completes pickup via `applyTaskCompletion` → `onTaskCompleted` (writes OFD + `syncJavaWithRetry("OUT_FOR_DELIVERY")`). Gate **before** that write when `task.Type == pickup` and `newStatus == completed`.
- `AdminCompleteTask` / `forceAssignAndComplete` / `forceProgressExisting` / `completePickupThenDrop` / skipJava stay **UNGATED**.
- `AcceptTrip` behavior unchanged. New test only proves it is not packed-gated.
- `javaOrderAPI` already has `GetOrderStatus`. `stubJavaOrder` already implements it.
- `eligibleOrderStatus` (`READY_FOR_DELIVERY`) is the pickup gate. Do **not** use `assignableOrderStatuses`.
- `classifyTaskUpdateError` / `classifyVerifyPickupError` use `errors.Is`.
- Existing pickup-complete tests with nil `javaClient` (must wire stub):
  - `TestUpdateTaskStatus_PickupCompletion_NotifiesCustomer`
  - `TestUpdateTaskStatus_PickupCompletion_FreezesDropDeadline`
  - `TestUpdateTaskStatus_PickupCompletion_UsesConfigXY`
  - `TestUpdateTaskStatus_PickupComplete_ConfigGetError_Succeeds`
- Reuse existing pickup fixture (`newTripServiceForTest` + accepted trip + pickup/drop tasks + `stubDERepo`) if present. There is no `TestVerifyPickup_*` or `TestAcceptTrip_*` today — add them in this style.
- Do not move `syncJavaWithRetry` `OUT_FOR_DELIVERY`.

- [ ] **Step 1: RED — service + classify tests**

Add sentinel `ErrOrderNotPacked` usage in tests first.

Service (`trip_service_test.go`):

- `TestVerifyPickup_BlockedUntilReadyForDelivery` — Java `PACKING`; after a valid scan, returns `ErrOrderNotPacked`.
- `TestUpdateTaskStatus_PickupComplete_BlockedUntilReadyForDelivery` — Java `CONFIRMED`; must **not** complete pickup and must **not** write OFD (`updateStatusCalled` / `dropDeadline` stay zero).
- `TestUpdateTaskStatus_PickupComplete_AllowedWhenRFD` — Java `READY_FOR_DELIVERY`; pickup completes.
- `TestAcceptTrip_NotGatedOnPacked` — Java `PACKING` (or CONFIRMED); `AcceptTrip` still succeeds. Do not change AcceptTrip production behavior.

Handlers (`trip_handlers_test.go`):

- Add `ErrOrderNotPacked` → 409 `ORDER_NOT_PACKED` to the `TestClassifyTaskUpdateError` table.
- Add `TestClassifyVerifyPickupError_OrderNotPacked` (or a classifyVerifyPickup table) mapping `ErrOrderNotPacked` → 409 `ORDER_NOT_PACKED`.

Wire **all** existing pickup-complete tests that have nil `javaClient` with `svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}` **as part of making the suite green after the gate exists** — do this in the GREEN step so RED still fails on the new tests. In RED, the new tests fail because `ErrOrderNotPacked` / gate do not exist.

```bash
go test -v ./internal/service/ -run 'VerifyPickup|PickupCompletion|AcceptTrip|PickupComplete'
go test -v ./internal/handlers/ -run 'classify'
```

Expected: new tests FAIL (missing sentinel / no gate). Existing classify table still PASS until the new row is added (new row FAIL). Existing PickupCompletion tests still PASS in RED (gate not implemented yet).

- [ ] **Step 2: GREEN — `requirePacked` rider-only**

```go
ErrOrderNotPacked = errors.New("order is not packed")
```

```go
func (s *TripService) requirePacked(ctx context.Context, orderID string) error {
	if s.javaClient == nil {
		return ErrOrderNotPacked
	}
	status, err := s.javaClient.GetOrderStatus(ctx, orderID)
	if err != nil {
		return err
	}
	if status != eligibleOrderStatus {
		return ErrOrderNotPacked
	}
	return nil
}
```

Call sites **only**:

1. `VerifyPickup` — after `validatePickupScan` succeeds.
2. `UpdateTaskStatus` — when completing a pickup task, **before** `applyTaskCompletion` (so no OFD write).

Do **not** call `requirePacked` from `AdminCompleteTask`, `completePickupThenDrop`, skipJava force-progress, or `AcceptTrip`.

Classify:

- `classifyTaskUpdateError`: `errors.Is(err, service.ErrOrderNotPacked)` → 409, `ORDER_NOT_PACKED`
- `classifyVerifyPickupError`: same

Wire existing nil-`javaClient` pickup-complete tests with `stubJavaOrder{status:"READY_FOR_DELIVERY"}`.

- [ ] **Step 3: Verify**

```bash
go test -v ./internal/service/ -run 'VerifyPickup|PickupCompletion|AcceptTrip|PickupComplete'
go test -v ./internal/handlers/ -run 'classify'
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/service/trip_service.go internal/service/trip_service_test.go internal/handlers/trip_handlers.go internal/handlers/trip_handlers_test.go
git commit -m "feat: block pickup until Java READY_FOR_DELIVERY"
```

---

## Task 4: UpdateEditByOrder

**Files:**
- Modify: `internal/repository/trip_repository.go`
- Modify: `internal/repository/trip_repository_test.go` **only** if a test can follow an existing non-Dynamo-fake pattern already in that file.

**Existing contract (do not invent):**
- `UpdatePayment` (`trip_repository.go`): `UpdateItem` on `PK=TRIP!{id}`, `SK=METADATA`; `SET payment = :payment, updated_at = :now`; condition `attribute_exists(PK) AND #status <> :completed AND #status <> :cancelled AND #status <> :distance_failed`; `ConditionalCheckFailedException` → `ErrTripTerminal`.
- `trip_repository_test.go` has **no** UpdatePayment fake-client test (only `TestReassign_SamePhone_RejectedBeforeTransaction` and `TestMarkOutForDeliveryUpdateExpression_PreservesFirstDeadline`). **Do not invent a Dynamo fake.**
- No second items/update HTTP path. No `ListOrders`.

- [ ] **Step 1: RED — expression helper test (no Dynamo fake)**

Match `TestMarkOutForDeliveryUpdateExpression_PreservesFirstDeadline`: extract the SET expression (and optionally the condition) to a package-level helper, then assert:

- SET includes `items`, `payment`, `tasks`, `updated_at`.
- Condition matches `UpdatePayment` (`attribute_exists(PK)` and the three terminal statuses).

There is no `updateEditByOrderUpdateExpression` today, so the test FAIL is the missing helper / missing method.

- [ ] **Step 2: GREEN — copy UpdatePayment**

```go
func (r *TripRepository) UpdateEditByOrder(ctx context.Context, tripID string, items []models.TripItem, payment *models.Payment, tasks []models.Task) error
```

Copy `UpdatePayment` logging, marshal, `UpdateItem` key, **same** `ConditionExpression` / terminal values, map `ConditionalCheckFailedException` → `ErrTripTerminal`. `UpdateExpression`: `SET items = :items, payment = :payment, tasks = :tasks, updated_at = :now`.

- [ ] **Step 3: Verify**

```bash
go test -v ./internal/repository/ -run 'UpdateEditByOrder|MarkOutForDelivery|Reassign_SamePhone'
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/repository/trip_repository.go internal/repository/trip_repository_test.go
git commit -m "feat: trip UpdateEditByOrder for packed snapshot"
```

---

## Task 5: POST /internal/v1/trips/edit-by-order

**Depends on:** T3 (trip service/handlers) + T4 (`UpdateEditByOrder`).

**Files:**
- Modify: `internal/service/trip_service.go` + `trip_service_test.go`
- Modify: `internal/handlers/trip_handlers.go` + `trip_handlers_test.go`
- Modify: `cmd/server/main.go`
- Do **not** add `/trips/items/update`. Do **not** add an API key.

**Existing contract (do not invent):**
- `UpdateTripPayment` + `UpdateTripPaymentByOrder` are the template: `GetByOrderID` (OrderIndex), no-trip → `{Updated:false, Reason:"no_active_trip"}` 200, `ErrTripTerminal` → `{Updated:false, Reason:"trip_terminal"}` 409, success `{Updated:true}` 200.
- `paymentFromOrder(JavaOrder{PaymentMethod, GrandTotal, Currency})` already exists.
- `tripRepoI` must gain `UpdateEditByOrder`. The **only** test stub is `stubTripRepo` in `trip_service_test.go` — add the method there. Record `editedItems` / `editedPayment` / `editedTasks`.
- `trip_handlers_test.go` has **no** payment/update httptest (classify only). Copy `TestUpdateTripPayment_*` **service** tests. A missing-`order_id` → 400 handler test is OK. Do not invent a full httptest suite.
- Route next to payment/update on the existing no-key `internal` mux:

```go
internal.HandleFunc("/trips/edit-by-order", tripHandlers.EditTripByOrder).Methods("POST", "OPTIONS")
```

- [ ] **Step 1: RED — service tests + missing order_id handler test**

Service (copy payment/update outcomes, **no rider-push assertions** — edit-by-order does not notify):

- `TestEditTripByOrder_NoTripIsNoop` — `GetByOrderID` nil → `Updated=false`, `Reason="no_active_trip"`; must not call `UpdateEditByOrder`.
- `TestEditTripByOrder_OverwritesItemsPaymentAndPickupZone` — body items (sku/name/image_url/quantity) become `TripItem`s with **packed quantity from the body**; `paymentFromOrder` snapshot; pickup task `DeliveryZone` set from `delivery_zone`. Stub records `editedItems` / `editedPayment` / `editedTasks`.
- `TestEditTripByOrder_IdempotentSecondCall` — second identical call still succeeds (`Updated=true`).
- Also cover terminal like payment/update: stub `updateEditByOrderErr = repository.ErrTripTerminal` → `Reason="trip_terminal"`, `Updated=false` (needed for 409).

Handler:

- Missing `order_id` → 400 `MISSING_FIELD` (mirror `UpdateTripPaymentByOrder`).

Body:

```json
{
  "order_id": "...",
  "payment_method": "...",
  "grand_total": 0,
  "currency": "ZMW",
  "delivery_zone": "...",
  "items": [{"sku": "...", "name": "...", "image_url": "...", "quantity": 1}]
}
```

```bash
go test -v ./internal/service/ -run 'EditTripByOrder|UpdateTripPayment'
go test -v ./internal/handlers/ -run 'EditTripByOrder|classify'
```

Expected: service tests FAIL (method missing / stub missing). Handler 400 test FAIL (handler missing).

- [ ] **Step 2: GREEN**

Add to `tripRepoI`:

```go
UpdateEditByOrder(ctx context.Context, tripID string, items []models.TripItem, payment *models.Payment, tasks []models.Task) error
```

Implement on `stubTripRepo` (record fields; return `updateEditByOrderErr`).

`TripService.EditTripByOrder`:

1. `GetByOrderID` — nil trip → `PaymentUpdateResult{Updated:false, Reason:"no_active_trip"}` (reuse the existing result type; do not invent a second result struct).
2. Map body items → `[]models.TripItem` (`Name`, `ImageURL`, `Sku`, `Quantity` from body — packed qty).
3. `payment = paymentFromOrder(...)`.
4. Copy `trip.Tasks`; set pickup task `DeliveryZone` from `delivery_zone`.
5. `tripRepo.UpdateEditByOrder(...)`. `errors.Is(..., repository.ErrTripTerminal)` → `{Updated:false, Reason:"trip_terminal"}`.
6. Success `{Updated:true}`. Idempotent (same writes).

Handler `EditTripByOrder`: decode body; empty `order_id` → 400 `MISSING_FIELD`; call service; 409 when `reason == "trip_terminal"`; else 200; JSON `{"updated":..., "reason":...}` like payment/update.

`cmd/server/main.go`: register the route next to `/trips/payment/update`. Internal `/internal/v1` stays without an API key.

- [ ] **Step 3: Verify**

```bash
go build -o /tmp/qcom ./cmd/server && go test ./internal/service/ ./internal/handlers/ ./internal/repository/
```

Expected: build OK, all three packages PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/service/trip_service.go internal/service/trip_service_test.go internal/handlers/trip_handlers.go internal/handlers/trip_handlers_test.go cmd/server/main.go
git commit -m "feat: POST /internal/v1/trips/edit-by-order packed snapshot"
```

---

## Done when

- Plan file is the first commit on the branch (force-add this file only).
- T1–T5 implemented TDD-green with the commits named above.
- `go build -o /tmp/qcom ./cmd/server` OK.
- `go test ./internal/service/ ./internal/handlers/ ./internal/repository/` PASS.
- One PR open against `main`.
