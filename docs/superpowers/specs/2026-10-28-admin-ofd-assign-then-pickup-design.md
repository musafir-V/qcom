# Admin out for delivery — assign, then pickup (Design)

**Date:** 2026-10-28
**Status:** Pending review
**Scope:** qcom and admin-dashboard. No BunzoApp, driver-app, or order-service changes.

## Summary

"Advance to out for delivery" on the order page can mark an order `OUT_FOR_DELIVERY` without a rider. The customer then sees that status with no delivery OTP. Track only returns the OTP once the trip is `accepted` or `out_for_delivery` and the drop is still open. The customer push that says "Share OTP …" is sent only when the pickup task is completed.

This change removes that status-only path. Ops either picks an eligible rider and the dashboard assigns them and then completes pickup, or the trip is already `assigned` or `accepted` and pickup complete accepts it if needed and completes pickup. Completing pickup sends the OTP push and moves the order to out for delivery. The one exception is a trip already `out_for_delivery` whose order status is still behind: that confirm only syncs the order status.

`ORD0748139127` is the incident. At 17:55 UTC the trip was still `created` with no rider. Admin pickup complete took the Java-only branch and wrote `OUT_FOR_DELIVERY`. The OTP `8205` stayed hidden until a later mark-delivered closed the drop.

## Goals

- A successful "Advance to out for delivery" from a pooled, `assigned`, or `accepted` trip completes pickup, so the customer push includes the drop OTP.
- When the trip is still in the pool, ops chooses a driver whose status is `eligible` and on duty at the trip's store, then the dashboard calls assign and then pickup complete.
- When the trip is `assigned` or `accepted`, ops confirms that rider. Pickup complete accepts an `assigned` trip, then completes pickup. The assign API is not called.
- When no trip exists, or the trip has no rider and pickup complete is called anyway, the order status does not change.
- The assignment cron is unchanged. Whichever of cron assign and admin assign writes first keeps the rider.

## Non-Goals

- No hold, lock, or new pre-check on the assignment cron.
- No reassign from this button. A rider busy on another order is a stop; ops uses the existing reassign flow.
- No change to mark delivered, drop preview, drop complete, or the phone-scoped pickup complete.
- No change to when track returns `otp`, or to OTP generation.
- No cash-limit filter. The assign API does not enforce the cash cap, and this list does not either. In-hand cash may be shown.
- No notes field on this confirm.
- No repair tool for orders already stuck in `OUT_FOR_DELIVERY` with a still-pooled trip.

## Approach

Follow the mark-delivered shape: a preview GET classifies the order, and the dashboard confirms one action. The server owns the classification. The dashboard sequences the two existing writes when the trip is pooled.

Rejected:

- **Java-only out for delivery** when no rider is on the trip. This is the incident. Pickup complete must refuse.
- **A hold while the picker is open.** The cron and `POST /admin/assign` already compete on "trip has no rider." First write wins. The loser fails.
- **Auto-completing pickup when admin assign loses the race.** That click fails. The rider who won does a normal accept, pickup, and drop. A later click sees an `assigned` trip and is a separate confirm.
- **Listing `free` and `offline` drivers.** `POST /admin/assign` requires status `eligible`. Mark delivered's wider list works only because that path flips status before assign. This button does not.

## Preview

`GET /api/v1/admin/orders/{orderId}/pickup/preview`

Same JSON shape as drop preview: `mode`, `reason`, `trip_id`, `java_status`, `trip_status`, `rider`, `candidates`.

`rider` is `{ phone, name, status }` when a rider is on the trip.

`candidates` is set only for `pick_rider`. Each item is `{ phone, name, status, in_hand_cash_zmw }`. The source is `DERepository.FindEligibleByStoreFIFO(trip.StoreID)`: status `eligible`, on duty at the trip's store (the same set the assignment cron assigns from). `free`, `offline`, and `busy` are absent. In-hand cash is display-only.

Evaluate in this order. The first matching row wins.

| # | Trip | Rider | Java | Mode | Reason |
|---|---|---|---|---|---|
| 1 | any | — | `CANCELLED` | `blocked` | `java_cancelled` |
| 2 | any, including none and `created` | — | `OUT_FOR_DELIVERY` or `DELIVERED` | `already_done` | — |
| 3 | `created`, `assigned`, or `accepted` | busy, `current_order_id` set and different from this order | any | `blocked` | `rider_busy_elsewhere` |
| 4 | `assigned` or `accepted`, `de_phone` set | that rider | any other | `force_progress` | — |
| 5 | `created`, `de_phone` set | that rider | any other | `force_progress` | — |
| 6 | `out_for_delivery` | any | `READY_FOR_DELIVERY` | `java_only` | — |
| 7 | `out_for_delivery` | any | anything else | `blocked` | `java_not_ready` |
| 8 | `created`, no `de_phone` | — | `READY_FOR_DELIVERY` | `pick_rider` | — |
| 9 | none, or any other status (`cancelled`, `completed`, `distance_failed`, …) | — | `READY_FOR_DELIVERY` | `blocked` | `no_trip` |
| 10 | none / `created` with no rider / closed | — | anything else | `blocked` | `java_not_ready` |

`java_only` remains only for a trip that is already `out_for_delivery` while Java is still `READY_FOR_DELIVERY`. That syncs the order status. It does not complete a task and it does not send a second OTP push.

The order page only offers this action when the order is `READY_FOR_DELIVERY`. If `force_progress` is invoked while Java is some other non-terminal status, pickup still completes first and `forceJavaOutForDelivery` then returns `ErrOrderNotDeliverable`. That order is unchanged from today.

## Pickup complete

`POST /api/v1/admin/orders/{orderId}/pickup/complete` uses the same classification.

| Mode | Result |
|---|---|
| `blocked` / `no_trip` or `created` with no rider | `ErrRiderRequired` → `400 RIDER_REQUIRED`. No Java write. No task write. No customer push. |
| `blocked` / `java_cancelled` | `ErrJavaOrderCancelled` → `409 ORDER_CANCELLED` |
| `blocked` / `java_not_ready` | `ErrOrderNotDeliverable` → `409 ORDER_NOT_DELIVERABLE` |
| `blocked` / `rider_busy_elsewhere` | `ErrRiderBusyElsewhere` → `409 RIDER_BUSY_ELSEWHERE` |
| `already_done` | `ErrAlreadyOutForDelivery` → `409 ALREADY_OUT_FOR_DELIVERY`. Trip unchanged. |
| `java_only` | `forceJavaOutForDelivery` only. |
| `force_progress` | Existing `forceProgressPickup`: if the rider is not already busy on this trip, attach them; if the trip is `assigned`, `Accept`; complete the pickup task with `skipJava=true`; then `forceJavaOutForDelivery` unless Java is already `OUT_FOR_DELIVERY` or `DELIVERED`. |

Completing the pickup task is what calls `notifyCustomer` with `ORDER_OUT_FOR_DELIVERY`. That push includes the drop OTP. The rider stays busy on the trip. Do not restore them to offline.

Java writes stay synchronous. Actor is `ADMIN:{username}` from the admin JWT `entity_id`. A Java refusal fails the POST. Do not use `syncJavaWithRetry` on this path.

`POST /admin/assign` is unchanged, except `ErrAdminAssignConflict` maps to `409 ORDER_NOT_ASSIGNABLE` (today it falls through to `500 ASSIGN_FAILED`). The trip must still be `created` with no rider, and the driver must be `eligible`.

## Dashboard

The out-for-delivery confirm loads pickup preview. There is still no notes field.

| Preview | What ops sees | Confirm |
|---|---|---|
| `blocked` | The reason. No trip: "No delivery trip yet. Out for delivery was not changed." Busy elsewhere: "Assigned rider is on another trip. Reassign this order first." | No confirm button. |
| `already_done` | "Already out for delivery." | No confirm button. |
| `java_only` | "Pickup is already done. This only updates the order to out for delivery." | Calls pickup complete only. |
| `force_progress` and `trip_status` is `assigned` | "Accept this trip for {name} and mark the order out for delivery? This completes pickup and sends the customer the OTP." | Pickup complete only. |
| `force_progress` and `trip_status` is `accepted`, or any other `force_progress` status | "Mark this order out for delivery for {name}? This completes pickup and sends the customer the OTP." | Pickup complete only. |
| `pick_rider` | A select of eligible riders: name, phone, in-hand cash. Empty list: "No eligible riders at this store." | Disabled until a rider is selected. |

`pick_rider` confirm:

1. `POST /api/v1/admin/assign` with `order_id` and the selected `driver_phone`, unchanged from the candidate.
2. If that returns success, `POST /api/v1/admin/orders/{orderId}/pickup/complete`.
3. If assign fails, do not call pickup complete. Reload the preview.
   - Preview is now `force_progress`: "This trip was just assigned to {name}. Out for delivery was not changed. They will pick up normally." Stop. Do not accept or complete pickup on this click.
   - Preview is still `pick_rider`: show the assign error and the refreshed list.
   - Preview is `blocked` or `already_done`: show that state.
4. If assign succeeds and pickup complete fails, show the pickup error and stop. The rider is now accepted and busy. The next time ops opens the dialog, preview is `force_progress` and confirm only calls pickup complete.

## Race

No new cron behavior. Cron `Assign` still requires `de_id` to be absent. Admin assign still requires the trip to be `created` with no rider and the driver to be `eligible`.

- Ops assign commits first: the cron write fails, the cron tries another rider or leaves this trip, and the dashboard continues to pickup complete.
- Cron assign commits first: admin assign fails, this click does not complete pickup, and the offered rider uses the normal accept and pickup flow. The OTP push goes out when that rider completes pickup.

## Testing

qcom, in `trip_service` tests:

- No trip, Java `READY_FOR_DELIVERY`: pickup complete returns `ErrRiderRequired`, no Java write.
- Trip `created` and no rider, Java `READY_FOR_DELIVERY`: same.
- Trip `assigned`, rider not busy elsewhere, Java `READY_FOR_DELIVERY`: trip becomes accepted, pickup task completes, Java gets one `OUT_FOR_DELIVERY` write, customer push is `ORDER_OUT_FOR_DELIVERY` and includes the drop OTP.
- Trip `accepted`: pickup completes, no extra accept, same push and Java write.
- Rider busy on a different order: `ErrRiderBusyElsewhere`, no task write, no Java write.
- Trip already `out_for_delivery`, Java `READY_FOR_DELIVERY`: one Java write, tasks unchanged, no customer push.
- Java already `OUT_FOR_DELIVERY` or `DELIVERED`: `ErrAlreadyOutForDelivery`, no writes.
- Preview for a `created` trip returns only `eligible` on-duty riders for that store, and does not return `free` or `offline`.

admin-dashboard:

- `pick_rider` confirm calls assign, then pickup complete.
- Assign failure does not call pickup complete. When the reloaded preview is `force_progress`, the copy says the trip was just assigned and out for delivery was not changed.
- Assign success plus pickup failure leaves a later confirm as pickup complete only.
- `assigned` confirm calls pickup complete and does not call assign.
- Empty candidate list disables confirm.

## Out of scope for the implementation plan

Do not edit `qcom/.claude/worktrees/` or `inventory/.worktrees/`. Do not change the assignment cron. Do not stage unrelated dirty files.
