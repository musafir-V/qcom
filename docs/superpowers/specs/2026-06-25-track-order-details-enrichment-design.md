# Track API — Order Details Enrichment (Design)

**Date:** 2026-06-25
**Status:** Approved (design)
**Scope:** Backend only (qcom). No frontend / order-service changes.

## Summary

Today the customer order details screen in BunzoApp is powered by the Java
order-service endpoint `GET /api/v1/orders/{orderNumber}`, which returns the rich
order payload (items, pricing, delivery, payment, refund, rating, status). qcom
separately exposes a thin tracking endpoint `GET /api/v1/orders/{orderId}/track`
that returns only `trip_status`, `de_name`, `otp`, `eta`.

We will turn the qcom track endpoint into a **superset**: it returns the full
order-details payload (verbatim, same field names) **plus** the delivery OTP and
two other trip-derived fields, so a single call can power the whole screen. The
frontend swap and any new UI are explicitly **out of scope** for this work — this
spec covers only the qcom backend change.

## Goals

- `GET /api/v1/orders/{orderId}/track` returns the complete order-details payload
  plus three added top-level fields: `otp`, `de_name`, `eta`.
- The delivery OTP (which lives only in qcom, on the trip's drop task) becomes
  available to the customer through this endpoint.
- The endpoint works for **every** order state, including delivered, cancelled,
  and brand-new orders that have no trip yet.

## Non-Goals

- No frontend / BunzoApp changes (endpoint swap, OTP display UI, etc.).
- No changes to the Java order-service.
- No `trip_status` field in the response (the screen uses the order `status`
  field, so trip status would be redundant). Dropped intentionally.
- No change to OTP generation, storage, or DE-side validation.

## Background / Current State

- **Order details API (source of truth for order data):** Java order-service,
  `GET /api/v1/orders/{orderNumber}` → `OrderResponse` (~26 top-level fields plus
  nested `delivery`, `items[]`, `refundSummary`, `rating`, `discountBreakdown`).
- **Track API (qcom):** `GET /api/v1/orders/{orderId}/track`, handler
  `TrackHandlers.Track` in `internal/handlers/track_handlers.go`. Current
  `TrackResponse` = `{ trip_status, de_name, otp, eta }`.
  - Current behavior we are **changing**:
    - No trip yet → `{ trip_status: "finding_driver", ... nulls }`.
    - Trip completed → `400 TRIP_COMPLETED`.
    - Trip cancelled → `400 TRIP_CANCELLED`.
- **`{orderId}` path param** is the human-readable order number (e.g.
  `ORD1162844363`). The handler already passes it to both the trip GSI lookup
  (`tripRepo.GetByOrderID`) and the Java client. Identifiers are consistent with
  the app, which keys everything on `orderNumber`.
- **Delivery OTP:** generated at trip creation (`assignment_cron.go` `randomOTP()`,
  4-digit string), stored on the drop `Task.OTP` in the trip's DynamoDB document.
  Order-service has no OTP. OTP is a delivery-handoff secret.
- **JavaOrderClient** (`internal/service/java_order_client.go`) already calls
  `GET /api/v1/orders/{orderId}` in `GetOrderStatus`, but decodes only the
  `status` field.
- **Consumers of track today:** none in BunzoApp (no track screen exists; tracking
  is rendered inline on the order details screen from the order payload's `status`).
  Changing the track contract is therefore low-risk.

## Approach

**Pass-through enrichment.** qcom fetches the full order JSON from order-service as
a loosely-typed object, looks up the trip, injects the three trip-derived fields,
and returns the merged JSON. Order-service remains the single source of truth for
all order fields; qcom owns only `otp`, `de_name`, `eta`.

Rationale: of the ~26 order fields, only these three are trip-derived. Everything
else (status, pricing, payment, refund, rating, coupons) exists only on
order-service; the trip's copies of delivery/items are duplicates of the same
data. A pass-through keeps order-service authoritative, requires no field-by-field
mirror in qcom, and lets future order fields flow through automatically with no
drift risk.

Rejected alternatives:
- **Typed mirror** (qcom defines a full struct for all order fields): high
  maintenance, silent field drop when order-service evolves.
- **Frontend merges two calls**: contradicts the "move to track" requirement, two
  mobile round-trips. (Also out of scope, since frontend is excluded.)

## Detailed Design

### 1. JavaOrderClient: fetch full order payload

Add a method that returns the raw order object, preserving all fields verbatim:

```go
// GetOrderRaw fetches the full order payload as an ordered, loosely-typed object.
// Returns (nil, nil) when the order does not exist (404).
func (c *JavaOrderClient) GetOrderRaw(ctx context.Context, orderID string) (map[string]json.RawMessage, error)
```

- GET `{baseURL}/api/v1/orders/{orderID}`.
- `404` → return `(nil, nil)` (caller maps to `ORDER_NOT_FOUND`).
- non-200 → return error (caller maps to upstream failure).
- 200 → decode body into `map[string]json.RawMessage` and return.

`map[string]json.RawMessage` is used (rather than `map[string]any`) so existing
field values are re-emitted byte-for-byte without numeric/precision reshaping.

### 2. TrackHandlers.Track: enrichment flow

Rewrite the handler body:

1. Read `orderId` from path; `400 MISSING_PARAM` if blank (unchanged).
2. `order, err := javaClient.GetOrderRaw(ctx, orderId)`
   - error → `502 FETCH_FAILED` ("Failed to fetch order").
   - `order == nil` → `404 ORDER_NOT_FOUND`.
3. `trip, err := tripRepo.GetByOrderID(ctx, orderId)`
   - error → **degrade gracefully**: log the error, treat `trip` as `nil`, and
     continue (tracking fields will be null). The order payload is the critical
     part and must still be returned.
4. Compute the three tracking fields (see §3).
5. Inject `otp`, `de_name`, `eta` keys into `order` (marshal each value to
   `json.RawMessage`; `null` when not applicable).
6. `200` with the merged object.

The `TripStatusCompleted`/`TripStatusCancelled` 400 branches and the
`finding_driver` no-trip branch are **removed**.

### 3. Tracking field rules (preserved from current handler)

Let `committed := trip != nil && (trip.Status == accepted || trip.Status == out_for_delivery)`.

- **`de_name`**: when `committed && trip.DEPhone != ""`, resolve via
  `deRepo.GetByPhone`; on success set the DE name. Else `null`.
- **`otp`**: when `committed && dropTask != nil && dropTask.Status == created`,
  set `dropTask.OTP`. Else `null`.
- **`eta`**: when `trip != nil && trip.CreatedAt != ""`, `computeETA(trip.CreatedAt)`
  (existing 15-minute promise, Africa/Lusaka). Else `null`.

`ETAPayload` shape is unchanged: `{ expires_at, remaining_minutes, is_delayed, message }`.

### 4. Response state matrix

| Order/trip state | `otp` | `de_name` | `eta` |
|---|---|---|---|
| No trip yet (CONFIRMED/PACKING/READY) | null | null | null |
| Trip created/assigned (finding driver) | null | null | computed |
| Accepted / out-for-delivery, drop open | shown | shown | computed |
| Delivered (drop complete) | null | shown→null* | computed |
| Cancelled | null | null | computed-or-null |

\* `de_name` follows the committed-state rule: it resolves while
`out_for_delivery`, and is `null` once the trip is `completed`. Net effect:
delivered orders return no OTP and no driver name, consistent with the current
hidden-after-delivery behavior.

### 5. Error handling

| Condition | Response |
|---|---|
| Blank `orderId` | `400 MISSING_PARAM` |
| Order not found (order-service 404) | `404 ORDER_NOT_FOUND` |
| Order-service error / non-200 | `502 FETCH_FAILED` |
| Trip lookup (DynamoDB) error | log + degrade: `200` with order payload, tracking fields null |
| DE lookup error | tracking field `de_name` null, request still `200` |

The principle: the order payload is required (hard-fail if it can't be fetched);
trip/DE enrichment is best-effort (soft-fail to null).

## Testing

Table-driven handler tests in `internal/handlers/track_handlers_test.go`, with the
Java client and repositories mocked, covering each state-matrix row plus error
paths:

1. No trip → order payload, all three tracking fields null.
2. Finding driver (created/assigned) → otp null, de_name null, eta present.
3. Accepted, drop open → otp present, de_name present, eta present.
4. Out-for-delivery, drop open → otp present, de_name present, eta present.
5. Delivered (drop complete) → otp null, de_name null, eta present.
6. Cancelled → otp null, de_name null.
7. Order not found → `404 ORDER_NOT_FOUND`.
8. Order-service error → `502 FETCH_FAILED`.
9. Trip lookup error → `200` with order payload + null tracking fields.
10. Merged payload preserves all original order fields verbatim (assert a
    representative set: `orderNumber`, `status`, `items`, `grandTotal`,
    `delivery`, `refundSummary`).

`computeETA` already has coverage; extend only if the refactor moves it.

## Risks & Considerations

- **Contract change to `/track`:** removes the 400/finding_driver short-circuits.
  Safe because nothing consumes track today; documented here for future readers.
- **Extra upstream call:** every track request now also hits order-service. This is
  the same call the screen already made before; net external load is unchanged once
  the frontend consolidates onto track. Java client already has a 10s timeout.
- **OTP exposure window:** unchanged — OTP only surfaces while a driver is
  committed and the drop is open. No widening of the secret's visibility.

## Out of Scope (follow-ups)

- Frontend: point the order details screen at the track endpoint and render OTP
  (and optionally `de_name` / `eta`). Separate task.
- Any consolidation/removal of the now-redundant inline status mapping in the app.
