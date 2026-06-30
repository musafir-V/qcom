# Trip Payment Update (COD → online & back) — API Contract

## Overview

The order-service (Java) is the source of truth for an order's payment method. When
an order's payment method changes **after** a qcom trip already exists — most
commonly a COD order that the customer then pays online — qcom must re-snapshot the
trip's payment so the rider stops being told to collect cash. This endpoint lets
order-service push that change to qcom.

- **Endpoint:** `POST /internal/v1/trips/payment/update` (qcom service)
- **Caller:** order-service, server-to-server.
- **Auth:** none at the application layer. The `/internal/v1` prefix is assumed to be
  reachable only from inside the private network (same assumption as the existing
  `/internal/v1/trips/cancel-by-order`). Do **not** expose it publicly.
- **Idempotent:** yes. Re-sending the same payload is safe; qcom always overwrites the
  trip's payment snapshot with the values in the request.

### When order-service should call this

Call it whenever an order's effective payment method changes and the order may already
be out for delivery, e.g.:

- A COD order is paid online (COD → `AIRTEL_MONEY` / `MTN_MONEY` / `CARD` / `BANK_TRANSFER`).
- An online order falls back to COD (online → `COD`).

It is harmless (a no-op) to call it for orders that don't have a trip yet, so erring on
the side of calling it is fine.

---

## Request

```
POST /internal/v1/trips/payment/update
Content-Type: application/json
```

```json
{
  "order_id": "ORD1162844363",
  "payment_method": "AIRTEL_MONEY",
  "grand_total": 250.00,
  "currency": "ZMW"
}
```

| Field | Type | Required | Meaning |
|---|---|---|---|
| `order_id` | string | yes | The order's stable id — the same value qcom stores as `trip.order_id` (i.e. `orderNumber` when present, else `orderId`). Used to look up the trip via the `OrderIndex` GSI. |
| `payment_method` | string | yes | New wire value. Recognized: `COD`, `AIRTEL_MONEY`, `MTN_MONEY`, `CARD`, `BANK_TRANSFER`. Only `COD` means the rider collects cash; anything else means already paid. Unknown non-empty values are treated as "no cash" (and warn-logged). |
| `grand_total` | number | yes | All-in amount (items + delivery + handling) — the cash to collect if `COD`. Overwrites the trip's `amount_zmw`. |
| `currency` | string | no | e.g. `ZMW`. Overwrites the trip's currency. |

> The mapping (`payment_method` → `collect_cash`) is the **same** logic qcom uses when
> it first creates the trip, so creation and update always produce identical snapshots.

---

## Responses

All responses are JSON `{ "updated": <bool>, "reason": <string> }`.

| HTTP | Body | Meaning | order-service action |
|---|---|---|---|
| `200` | `{"updated": true, "reason": ""}` | Trip found and payment re-snapshotted. | Done. |
| `200` | `{"updated": false, "reason": "no_active_trip"}` | No trip exists for this order yet (still being picked). The trip will be created later from the already-updated order, so it will be correct. | Treat as success / no-op. |
| `409` | `{"updated": false, "reason": "trip_terminal"}` | The trip is already `completed` / `cancelled` / `distance_failed`. Payment is frozen — by then COD cash may already have been collected and accrued to the rider. qcom did **not** change anything. | Do not retry as if it failed; log/alert if a paid-online order is somehow already delivered-as-COD. |
| `400` | `{ "error": { "code": "MISSING_FIELD" \| "INVALID_REQUEST", ... } }` | `order_id` / `payment_method` missing, or body not valid JSON. | Fix request. |
| `500` | `{ "error": { "code": "PAYMENT_UPDATE_FAILED", ... } }` | Unexpected qcom/DynamoDB error. | Safe to retry with backoff (idempotent). |

### State semantics summary

| Trip state when called | Behavior |
|---|---|
| No trip yet | `200 no_active_trip` (no-op; creation will use the updated order) |
| `created` / `assigned` | Payment updated. No push (rider not engaged yet); their 10s poll picks it up. |
| `accepted` / `out_for_delivery` | Payment updated **and** a quiet `PAYMENT_UPDATED` push is sent if the cash requirement materially changed (so the rider is corrected immediately). |
| `completed` / `cancelled` / `distance_failed` | `409 trip_terminal` — rejected, nothing changed. |

The terminal-state rejection is enforced atomically via a DynamoDB conditional write,
so a trip that is completed at the same instant the update arrives is never silently
stripped of an already-collected COD amount.

---

## Rider sync (informational)

Riders see payment via the trip JSON from `GET /api/v1/de/trip` (polled every ~10s and
on push). On a successful update for an **active** trip whose cash requirement changed,
qcom additionally sends an FCM push to the assigned rider:

```json
{
  "type": "PAYMENT_UPDATED",
  "trip_id": "<id>",
  "order_id": "<id>",
  "collect_cash": "true" | "false"
}
```

with a `Title`/`Body` heads-up (e.g. "Customer paid online — no cash to collect"). The
push is **Normal** priority (quiet channel, not the loud order-assignment channel); its
only job is to make the driver app re-poll and surface a correction immediately.
Polling remains the backstop if the push is missed.
