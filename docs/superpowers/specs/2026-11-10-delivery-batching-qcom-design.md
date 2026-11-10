# Delivery batching — qcom (Design)

**Date:** 2026-11-10
**Status:** Pending review
**Scope:** qcom only. Companion specs: `admin-dashboard/docs/superpowers/specs/2026-11-10-delivery-batching-admin-design.md`, `qcom/docs/superpowers/specs/2026-11-10-delivery-batching-driver-app-design.md` (paste that file into the driver-app repo). No order-service / BunzoApp changes.

## Summary

Ops batches an order onto a rider who already has a live trip at the same darkstore. qcom closes the involved live trip(s) as `inactive` (superseded, not cancelled) and creates a **new** trip that **copies tasks with their current status**. Pickups stay in merge order. Open drops are reordered by a nearest-neighbour tour (Google Distance Matrix, haversine fallback). The rider does not accept again. Java order status is per-order and is never rewritten for a status already applied. Payout is one fare on the surviving trip’s tour km, paid when that trip completes.

Today a trip is one order, one pickup, one drop. `GetCurrentTrip` keys off `current_order_id`. `GetByOrderID` is OrderIndex `Limit 1`. `PickupTask()` / `DropTask()` return the first of each type. Assignment cron treats “any trip on OrderIndex” as owned. All of that is wrong once two orders share a rider.

## Goals

- Admin can attach this order to a same-store rider who already has a live trip, including `out_for_delivery`.
- No cap on how many orders share a surviving trip.
- Steal / split is allowed: pulling this order off rider 2 onto rider 1 rebuilds rider 2’s leftover (or frees them).
- Completed tasks are not redone. Java is not rewritten for a status the order already has.
- Customer track keeps working (OTP, driver, 15-minute ETA) by following the successor trip. `inactive` is not treated as cancelled/completed.
- Flag off until the driver app can walk N tasks. Merge onto an old app is refused.

## Non-Goals

- Automatic / cron batching.
- Cross-store batches.
- Google waypoint optimization API.
- Live rider GPS as the tour origin.
- Customer-facing “batched” copy or extra push.
- Handoff / return-to-store task when a stolen bag is already picked up (ops problem).
- Changing order-service transitions (no `OFD → READY`).
- Paying per drop. Pay once when **this** trip completes.
- Blocking merge on cash cap (warn only).
- Unmerge button (steal the order back with Batch, or Reassign the whole trip).

## Feature flag

DynamoDB singleton `PK=CONFIG`, `SK=BATCHING_V1`:

```go
type BatchingConfig struct {
    Enabled               bool   `json:"enabled" dynamodbav:"enabled"`
    MinDriverAppVersion   string `json:"min_driver_app_version" dynamodbav:"min_driver_app_version"`
}
```

- `enabled=false` (default): candidates and confirm return `403 BATCHING_DISABLED`.
- `MinDriverAppVersion` is semver (`"1.4.0"`). A DE with empty or lower `last_app_version` is not a legal merge target or source.

Stamp `last_app_version` on the DE from header `X-App-Version` on every authenticated DE request (at least `GET /api/v1/de/trip`). Missing header → leave the stored value; if still empty, treat as old.

## Model

### TripStatus

Add `TripStatusInactive = "inactive"`. Terminal. Not cancelled: the order is still live on a successor.

Legal transitions into `inactive` from `created`, `assigned`, `accepted`, `out_for_delivery`. No transitions out.

A merged trip is **created already** `accepted` or `out_for_delivery`. That is a create-in-state, not a transition from `created`.

`IsValidTripTransition`: add those `→ inactive` edges. Do not add `out_for_delivery → accepted` on the same trip.

### Trip fields (additive)

- `order_ids []string` — all live orders on this trip, pickup merge order.
- `order_id` — keep as `order_ids[0]` for old readers.
- `supersedes_trip_ids []string` — trips this one replaced.
- `superseded_by_trip_id string` — set on `inactive` trips.
- `distance_method string` — `"google"` or `"haversine"` for the frozen tour.
- `distance_km` — frozen NN tour km used for payout (not the sum of old store→customer legs).
- `drop_deadline` on the trip — leave as the **earliest** open drop deadline (driver countdown). Per-drop deadlines live on tasks.

### Task fields (additive)

Every task has `order_id` (required for new/copied tasks).

Pickup tasks also copy: `items []TripItem`, `delivery_zone`.

Drop tasks also copy: `otp`, `recipient_name`, `payment *Payment`, `drop_deadline *int64` (unix seconds, the original trip’s deadline for that order).

Do not use `PickupTask()` / `DropTask()` for batched trips. Add helpers:

- `TasksForOrder(orderID)`
- `PickupForOrder(orderID)` / `DropForOrder(orderID)`
- `FirstIncompleteTask()` — first in `tasks` with status ≠ `completed`
- `OpenPickups()` / `OpenDrops()`

`ActualDeliveryMinutes` is unused for batched payout (tour km + per-drop deadlines instead).

### DE

- `GetCurrentTrip` loads by `current_trip_id` (GetByID). If missing, fall back to `current_order_id` + live-trip lookup for old rows.
- Treat trip status `completed`, `cancelled`, **`inactive`** as stale → return `trip: null`.
- `current_order_id` = `FirstIncompleteTask().OrderID` after every merge/progress (display / cash / admin). Empty when no live trip.
- `last_app_version string`

### OrderIndex

One DynamoDB item cannot hold N `trip_order_id` values. Keep `METADATA.trip_order_id = order_ids[0]`. For **every** `order_id` on a live trip, write a sibling:

```
PK = TRIP!{tripID}
SK = ORDIDX!{orderID}
trip_order_id = {orderID}   // OrderIndex GSI
trip_id, status
```

On `inactive` / cancel / complete, set `status` on every `ORDIDX` sibling to that terminal status (or delete the siblings). Assignment and track must not see them as live.

**Live trip for an order:** query OrderIndex, load distinct `trip_id`s, `GetByID`, pick the unique trip whose status is `created|assigned|accepted|out_for_delivery`. If several, newest `created_at` wins and log an error.

Replace every `GetByOrderID` call site that means “the live trip” with this helper (`LiveTripByOrderID`). `ChooseTripForLegs` additionally considers `completed` and **skips `inactive`** the same way it skips `cancelled`.

## Merge algorithm

Confirm is one DynamoDB transaction (plus Maps calls **before** the write).

Inputs: `order_id` (the page), `target_de_phone`, optional `expected_updated_at` map `trip_id → rfc3339` for optimistic lock.

### 1. Load

- This order’s live trip, if any (`sourceTrip`). May be nil.
- Target DE; must be `busy` with `current_trip_id` set. Load `targetTrip`.
- If `sourceTrip` exists and `sourceTrip.DEID != target DE`, that is a steal. `loserTrip = sourceTrip`.
- If `sourceTrip` is the target trip, this order is already on that rider → `409 ALREADY_ON_RIDER`.

### 2. Gates (before Maps)

| Condition | HTTP |
|---|---|
| Flag off | `403 BATCHING_DISABLED` |
| Target or loser `last_app_version` < min | `409 APP_TOO_OLD` |
| Target trip missing / terminal | `409 NO_LIVE_TRIP` |
| `targetTrip.StoreID` ≠ this order’s store (Java `storeId`, or source trip store) | `409 WRONG_STORE` |
| Target DE not `busy` | `409 DRIVER_NOT_BUSY` |
| Any `expected_updated_at` mismatch | `409 STALE_MERGE` |
| This order Java `CANCELLED` or `DELIVERED` | `409 ORDER_NOT_BATCHABLE` |

Do **not** refuse OFD, already-batched, or cash over cap.

### 3. Build task lists

Copy every task from `targetTrip` with status preserved.

If `sourceTrip` is nil: synthesize pickup+drop for this order the same way assignment cron `createTrip` does (items, zone, OTP, payment, coords, drop deadline). Both tasks `created`.

If `sourceTrip` is set: append **that trip’s pickup tasks in existing order**, then we will append/reorder drops separately. Deduplicate by `order_id`: this order’s tasks come from source, not a second copy from target.

Loser leftover: `loserTrip` tasks whose `order_id` is **not** this order. If none, loser is freed (no leftover trip).

### 4. Pickup sequence

Concat: target trip’s pickups (existing order) then incoming pickups (this order’s, or the stolen trip’s pickup block for this order only). Status preserved.

### 5. Drop sequence (NN)

Only **incomplete** drops are reordered. Completed drops stay in the list after pickups, in the order they were already completed.

Origin:

1. If any pickup is still not `completed` → store lat/lng.
2. Else if a drop is `completed` → that drop’s lat/lng (the latest `completed_at`).
3. Else → keep the **target trip’s current first open drop** first; origin for the rest is that drop’s customer lat/lng.

Nearest neighbour among remaining open drops: repeatedly pick the closest unused drop from the current origin, then origin = that drop. Distance: `DistanceService.DistanceKM` (Google driving). On **any** Google error (`ErrNoRoute` or upstream), switch the **whole tour** to haversine (`models.HaversineDistance` / 1000) and set `distance_method=haversine`. Do not write `distance_failed`. Do not abort the merge.

Tour km = sum of legs origin→d1→d2→… Freeze on the new trip as `distance_km`. Re-run fare engine for `base_pay_zmw` / SLA fields from that km (same as createTrip). `on_time` is computed at completion, not now.

### 6. Reached reset

If the new list has any incomplete pickup, set every **incomplete** drop to `created` and clear `reached_at`. Completed drops untouched.

### 7. New trip status

- Any pickup not `completed` → `accepted`. No `accept_deadline`. No `ORDER_ASSIGNED` push.
- Else → `out_for_delivery`. Recompute trip-level `drop_deadline` as min of remaining drop deadlines.

Do not write Java on merge.

### 8. Persist (transaction)

- Put new trip METADATA + ORDIDX siblings for every `order_ids`.
- Set old target (and loser, if steal) to `inactive`, `superseded_by_trip_id`, stamp `ORDIDX` siblings inactive.
- If loser leftover: put leftover trip (same rules: copy remaining tasks, NN on **its** remaining open drops, status from remaining pickups, `supersedes` the loser). Attach leftover to loser DE.
- If no leftover: free loser DE (`eligible`/`free` per existing cancel path), clear `current_trip_id` / `current_order_id`.
- Target DE: `current_trip_id` = new trip, `current_order_id` = first incomplete order, stay `busy`.
- Condition: each closed trip `updated_at` matches preview; target DE `current_trip_id` still the old trip.

Loser leftover also gets `TRIP_UPDATED`. Target gets `TRIP_UPDATED`.

FCM `event_type=TRIP_UPDATED`, recipient DE, data: `trip_id` (new), `supersedes_trip_id`. Channel stays `order-alert-v3`. Not in `eventMinPriority` critical map unless you want it — **do** send it; default priority is fine.

### 9. Split settle

If leftover (or, after cancel-one, the surviving trip) has **zero open drops**, mark it `completed` in the same flow, pay tour km, increment `delivery_rank_of_day` once, free the DE if they have no other live trip. Inactive trips never pay.

## Task progression (existing `POST /api/v1/trip/{tripId}/task/{taskId}/status/update`)

Keep completing **by task_id**.

### Trip-status gate (replace `validateTaskAgainstTripStatus`)

- Pickup complete: trip must be `accepted`. After this pickup, if **any** pickup is still open, trip stays `accepted`. If **all** pickups are now completed, transition `accepted → out_for_delivery` (existing `MarkOutForDelivery` with min remaining drop deadline).
- Drop complete: trip must be `out_for_delivery`.
- Drop reached: unchanged geofence, keyed by **that** drop task.

### Java (per `task.order_id`)

- Pickup complete: if Java is `READY_FOR_DELIVERY`, write `OUT_FOR_DELIVERY`. If already `OUT_FOR_DELIVERY` or `DELIVERED`, skip (no-op). Do not write `READY`.
- Drop complete: write `DELIVERED` for **that** order only. Invoice/COD_COLLECTED stay Java’s problem on that write. If more drops are open, trip stays `out_for_delivery`; DE stays `busy`; **do not pay**.
- Last drop complete: `CompleteTripAndFreeDE`, payout from frozen `distance_km` / fare already on the trip, `on_time` = every drop’s `completed_at` ≤ that drop’s `drop_deadline` (missing deadline → treat as on-time for that drop). `delivery_rank_of_day` increments **once**. COD in-hand += that drop’s `payment` if `collect_cash` (each COD drop, including the last).

### Inactive trip_id

If the trip is `inactive` (or DE `current_trip_id` ≠ this id): `409 TRIP_INACTIVE`. No task write.

`requirePacked` still gates pickup complete for that order (Java must be `READY_FOR_DELIVERY` or already `OUT_FOR_DELIVERY`).

## Cancel

`POST /internal/v1/trips/cancel-by-order` (Java already calls this with one order number):

- Live trip with `order_ids` length 1: existing cancel (trip `cancelled`, free DE).
- Length > 1: same as steal leftover — close live trip `inactive`, successor without that order’s pickup+drop, NN remaining drops, settle if nothing open. If that order’s pickup was already completed, still drop the tasks; no return-to-store task. Java cancel remains fail-open.

Do **not** set the trip to `cancelled` for the surviving orders.

## Admin complete by order

`POST /admin/orders/{orderId}/pickup/complete` and drop complete: operate on **that order’s** pickup/drop on the **live** trip. Completing “the” first pickup/drop on the driver’s trip is a bug once N>1.

OTP skipped on admin drop (unchanged). Java: same per-order rules as rider complete. If other drops remain, do not complete the trip.

Reassign (`POST /admin/trips/{trip_id}/reassign`): whole surviving trip. Pass every `order_id` through DE `current_*` writes. Do not split.

`POST /admin/assign`: unchanged (pooled trip → eligible idle DE). Not a batch.

## Payment edit

`POST /internal/v1/trips/payment/update` (by order): update **that order’s drop** `payment`. Keep `trip.payment` as a rollup: `collect_cash` true if any open drop collects cash; `amount_zmw` = sum of those. Driver app prefers task-level payment.

`in_hand_cash` increases when a COD drop **completes**, not at merge.

Cash cap: candidates payload includes `cash_exceeds` if `in_hand + remaining COD on the resulting trip > limit`. Confirm does not 409 on that.

## Track / PSTN / disputes

`LiveTripByOrderID`. If the only hits are `inactive`, follow `superseded_by_trip_id` until live or missing.

Track `tripTerminal` is only `completed` or `cancelled`. **`inactive` is not terminal.** OTP comes from **this order’s** drop task, not `DropTask()`. Driver revealed when the live trip is `accepted` or `out_for_delivery`. ETA formula unchanged (15 minutes from Java `createdAt`).

## Assignment cron

`needsCreate` is true only when `LiveTripByOrderID` is nil. An `inactive` row must not count as owned. After merge, ORDIDX for both orders points at the successor, so cron must not spawn a singleton.

## GET /api/v1/de/trip

Return the live trip JSON with additive fields (`order_ids`, per-task `order_id` / `items` / `payment` / `drop_deadline`). Keep top-level `order_id`, `items`, `payment` as rollups so a briefly old app does not nil-crash; it will still be wrong operationally, which is why merge is gated on version.

## Admin HTTP

### `GET /api/v1/admin/orders/{orderId}/batch/candidates`

Auth: admin. 403 if flag off.

Query live same-store DEs whose `current_trip_id` is a live trip. Exclude the DE who already owns this order.

Response:

```json
{
  "source_trip_id": "…",
  "source_trip_updated_at": "2026-11-10T09:55:00+02:00",
  "source_rider_name": "Bwalya",
  "candidates": [
    {
      "phone": "+2609…",
      "name": "Ada",
      "trip_id": "…",
      "trip_updated_at": "2026-11-10T10:00:00+02:00",
      "app_ok": true,
      "cash_exceeds": false
    }
  ]
}
```

`source_*` describes **this order’s** live trip, if any (including pooled `created`). Omitted when this order has no trip. `source_rider_name` is who currently holds it (split one-liner). Each candidate is a **target** rider; qcom already excludes the DE who owns this order.

### `POST /api/v1/admin/orders/{orderId}/batch`

```json
{
  "target_de_phone": "+2609…",
  "expected_updated_at": {
    "<target_trip_id>": "<rfc3339>",
    "<source_trip_id>": "<rfc3339>"
  }
}
```

`expected_updated_at` required for every trip that will go `inactive`. 409 `STALE_MERGE` on mismatch.

Success `200`:

```json
{
  "trip_id": "new",
  "order_ids": ["A", "B"],
  "status": "accepted",
  "distance_method": "google",
  "warnings": ["cash_exceeds"]
}
```

`warnings` may include `cash_exceeds`, `haversine_fallback`. Never includes `app_too_old` (that is a 409).

## Error codes

| Code | Status | When |
|---|---|---|
| `BATCHING_DISABLED` | 403 | flag off |
| `APP_TOO_OLD` | 409 | target or source rider below min version |
| `STALE_MERGE` | 409 | optimistic lock |
| `TRIP_INACTIVE` | 409 | task update on superseded trip |
| `ALREADY_ON_RIDER` | 409 | this order already on target trip |
| `WRONG_STORE` | 409 | different darkstore |
| `NO_LIVE_TRIP` | 409 | target has no live trip |
| `DRIVER_NOT_BUSY` | 409 | target not busy |
| `ORDER_NOT_BATCHABLE` | 409 | Java cancelled/delivered |

## Testing (qcom)

- Merge two `accepted` singletons → 2 pickups (merge order, both open) + NN drops; old trips `inactive`; DE `current_trip_id` swapped; no accept push.
- Merge onto OFD (pickup A completed) + READY B → new trip `accepted`; pickup A stays `completed`; Java A not written; pickup B then OFD B; reached on A’s drop cleared.
- Both OFD, no drop done → keep target’s current drop first; NN the rest from that customer; trip stays `out_for_delivery`.
- Steal B off a 2-order trip → leftover rebuilt for loser; both DEs get `TRIP_UPDATED`.
- Steal last order off loser → loser freed; no leftover trip.
- No source trip → synthesize tasks in the same confirm; cron does not create a duplicate.
- Google `ErrNoRoute` → haversine tour, merge succeeds, `distance_method=haversine`, no `distance_failed`.
- Stale `updated_at` → 409, nothing inactive.
- Complete on old trip_id → 409 `TRIP_INACTIVE`.
- Drop A complete while B open → Java A `DELIVERED`, trip still OFD, no payout.
- Last drop → pay frozen km, one rank, `on_time` AND of per-drop deadlines.
- Cancel one order in a 2-order trip → leftover successor, not `cancelled`.
- Admin pickup/drop by order hits that `order_id`’s tasks.
- Track after merge returns OTP from this order’s drop; `inactive` does not hide ETA.
- Cron tick after merge does not `needsCreate` for either order.
- Flag off / old app → 403 / 409.
- Cash over cap still 200 with `warnings: ["cash_exceeds"]`.
