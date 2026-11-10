# Delivery batching — driver app (Design)

**Date:** 2026-11-10
**Status:** Pending review
**Scope:** driver app only. qcom owns merge, Java status, and payout. This file lives in the bunzo monorepo because driver-app is a separate checkout — copy it into that repo as `docs/superpowers/specs/2026-11-10-delivery-batching-driver-app-design.md`. Companion: `qcom/docs/superpowers/specs/2026-11-10-delivery-batching-qcom-design.md`.

## Summary

A trip can hold **N pickups then N drops** after ops batches another order onto you. qcom copies tasks **with their status**. You are not asked to redo a completed pickup or drop. You do not Accept a new offer — the trip id changes under you. Complete **by `task_id`**. Current work is the **first incomplete task** in `tasks` order.

Until this app version ships, qcom **refuses** to batch onto you. Send `X-App-Version` on every authenticated request.

## Goals

- Render every task on `GET /api/v1/de/trip`, in list order.
- Current task = first task whose `status` is not `completed`.
- Complete / reach using the existing `POST /api/v1/trip/{tripId}/task/{taskId}/status/update` with **that** `task_id`.
- On `TRIP_UPDATED` push or `409 TRIP_INACTIVE`, refetch `GET /de/trip` and bind UI to the new `trip_id`.
- Per-order OTP and cash on the drop being completed, not a single trip-level payment.
- Send `X-App-Version` so qcom can gate merge.

## Non-Goals

- Deciding who to batch (admin only).
- Reordering tasks on the client (server list is the tour).
- Accept / reject of a merged trip (there is no assigned-offer for a merge).
- Rewinding order status or calling Java.
- A map of the full tour in v1 (optional later). Follow the current task’s address.
- Handling `inactive` trips as current work (server returns `trip: null` or 409).

## Version header

On every DE-authenticated request (login, `GET /de/trip`, task update, location if any):

```
X-App-Type: de
X-App-Version: {marketingSemver}
```

Use the store/build marketing version (`1.4.0`), not a build number. qcom stores it on the DE as `last_app_version`. The min version is a server config; bump this app’s version when this spec ships so ops can set `min_driver_app_version` to that value.

## `GET /api/v1/de/trip`

Unchanged path. Additive JSON. Ignore unknown fields.

```json
{
  "trip": {
    "trip_id": "…",
    "order_id": "ORD-A",
    "order_ids": ["ORD-A", "ORD-B"],
    "store_id": "…",
    "status": "accepted",
    "distance_km": 4.2,
    "drop_deadline": 1731234567,
    "items": [{ "name": "…", "quantity": 1 }],
    "payment": { "collect_cash": true, "amount_zmw": 80, "currency": "ZMW" },
    "tasks": [
      {
        "task_id": "p-a",
        "type": "pickup",
        "status": "completed",
        "order_id": "ORD-A",
        "address": "Store …",
        "lat": -15.4,
        "lng": 28.3,
        "delivery_zone": "Blue Rack 2",
        "items": [{ "name": "Milk", "quantity": 1, "image_url": "…", "sku": "…" }]
      },
      {
        "task_id": "p-b",
        "type": "pickup",
        "status": "created",
        "order_id": "ORD-B",
        "address": "Store …",
        "lat": -15.4,
        "lng": 28.3,
        "delivery_zone": "Red Rack 1",
        "items": [{ "name": "Bread", "quantity": 2 }]
      },
      {
        "task_id": "d-b",
        "type": "drop",
        "status": "created",
        "order_id": "ORD-B",
        "phone": "+260…",
        "address": "Customer B",
        "lat": -15.41,
        "lng": 28.32,
        "otp": "1234",
        "recipient_name": "Bwalya",
        "payment": { "collect_cash": true, "amount_zmw": 80, "currency": "ZMW" },
        "drop_deadline": 1731234567
      },
      {
        "task_id": "d-a",
        "type": "drop",
        "status": "created",
        "order_id": "ORD-A",
        "phone": "+260…",
        "address": "Customer A",
        "otp": "5678",
        "payment": { "collect_cash": false, "amount_zmw": 45 }
      }
    ]
  }
}
```

`trip` may be `null` (no live job).

### How to read the payload

| Need | Read |
|---|---|
| What to do now | First `tasks[]` element with `status != "completed"` |
| Which order | that task’s `order_id` |
| Pickup bag / rack | that pickup’s `items`, `delivery_zone` |
| Drop OTP / cash | that drop’s `otp`, `payment` |
| Trip-level `payment` / `items` / `order_id` | **Fallback only** when `task.order_id` is absent (old qcom). If `order_id` is present on tasks, **do not** collect `trip.payment` as a lump. |

`status` on the trip:

- `assigned` — existing accept/reject offer. Unchanged. Merge never leaves you in `assigned`.
- `accepted` — still have at least one open pickup. Go to the store for the current pickup. Completing the **last** open pickup is what qcom moves to `out_for_delivery`; the app just refetches after the 200.
- `out_for_delivery` — all pickups done. Current task is a drop (reach + OTP).

Do not hide completed tasks; grey them as done so the rider sees the remaining list. Tapping a completed task is a no-op.

## Completing work

Existing endpoint:

`POST /api/v1/trip/{tripId}/task/{taskId}/status/update`

Body unchanged: `{ "status": "reached"|"completed", "otp", "photo_s3_key", "lat", "lng" }`.

Rules:

- Path `tripId` **must** be `trip.trip_id` from the last successful GET. Do not cache an older id.
- `taskId` **must** be the current incomplete task (or that task after reach). Do not send “the pickup” / “the drop” by type.
- Pickup complete: no OTP. Photo optional as today.
- Drop `reached`: lat/lng required as today.
- Drop `completed`: OTP for **this** drop (`task.otp`). Cash UI from **this** drop’s `payment`.

After 200, `GET /de/trip` again (or use the returned payload if you already do). The next current task may be another pickup, another drop, or `trip: null` if the job is finished.

## `409 TRIP_INACTIVE`

qcom returns this when ops batched (or split) while you still had the old `trip_id`.

App behaviour:

1. Do not show a generic fatal error that implies the drop failed and must be done again.
2. Immediately `GET /api/v1/de/trip`.
3. Bind to the new `trip_id`.
4. If the task you were completing is already `completed` on the new trip, advance to the new current task (qcom copied status; you do not redo it).
5. If it is still open, retry the **same status update** on the **new** trip’s matching `task_id` (same `order_id` + type). Match by `order_id` + `type`, not by old `task_id` (ids are new).

Copy:

> Job updated. Reloading your stops.

## `TRIP_UPDATED` push

FCM `event_type=TRIP_UPDATED`. Data includes `trip_id` (the new live trip).

On receive (foreground or when the trip screen opens from the notification): `GET /de/trip` and replace local trip state. Same copy as 409 if the rider is mid-screen.

Do not run Accept. Do not show a new offer card.

Treat unknown `event_type` values as “refetch current trip” if you already do that for `PAYMENT_UPDATED`; at minimum handle `TRIP_UPDATED` explicitly.

`PAYMENT_UPDATED` still means cash on a drop changed (customer paid online). After refetch, that drop’s `payment.collect_cash` is false. If you were showing collect on that order, update it.

## Accept / reject

Unchanged for `status=assigned` singleton offers from cron.

If `status` is `accepted` or `out_for_delivery`, there is no accept button. A merge never requires Accept.

## UI

Minimum v1:

1. **Stop list** — all tasks in order, labelled Pickup / Drop + last 4 of `order_id` (or full order number). Status chip: done / now / later.
2. **Now card** — current task only: address, nav link, rack + items for pickup, recipient + phone + OTP + cash for drop.
3. After completing current, the next incomplete becomes Now.

Do not assume exactly two tasks. Do not assume the first task is pickup and the last is drop (a fully picked batch is drops-only; a merge can insert pickups **before** remaining drops).

If qcom cleared `reached` on a drop (ops added a pickup while you were at a customer), that drop is `created` again. You must reach it when it becomes current. Do not complete OTP off the old reached state.

## Errors (existing, still apply)

- Pickup while trip is `out_for_delivery` should not happen if you follow current-task. If the server says prerequisite incomplete, refetch.
- Wrong OTP: stay on the same drop.
- `requirePacked`: pickup complete may fail until the store has packed that order. Show the existing not-packed message; stay on that pickup.

## Testing (driver app)

- Singleton trip (1 pickup, 1 drop) still works with `task.order_id` present or absent.
- Two pickups then two drops: cannot open drop OTP until both pickups are completed (server will 4xx; UI should not offer drop as Now).
- First pickup already `completed` in GET: Now is the second pickup; do not show a pickup-complete button for the first.
- Mid-drop 409 `TRIP_INACTIVE`: refetch; if that drop is still open on the new trip, OTP still works; if a pickup was inserted, Now is that pickup and the drop is later and `created` (not reached).
- `TRIP_UPDATED` while on the Now card: list and Now replace without crashing; `trip_id` in subsequent POSTs is the new one.
- COD only on the drop whose `payment.collect_cash` is true; prepaid drop does not show collect.
- `X-App-Version` present on login and trip GET (Charles / log).
- `trip: null` after last drop: idle / waiting screen, same as today.
