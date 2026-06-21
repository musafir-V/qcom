# Central Notification Service

The **qcom notification service** is the single place Bunzo sends mobile push notifications. Other backends do **not** talk to Firebase directly — they call qcom with *who* to notify and *what* to say. qcom owns FCM credentials, device token storage, token invalidation, and delivery.

**Production public base URL:** `https://api.bunzodelivery.com`

---

## What qcom owns vs what callers own

| Concern | Owner |
|---|---|
| FCM credentials & send | qcom |
| Device token storage | qcom (DynamoDB) |
| Token registration from apps | qcom (`PUT /api/v1/device-token`) |
| Clearing stale/invalid tokens | qcom (automatic on FCM error) |
| **Who** to notify (`recipient_type`, `recipient_id`) | caller |
| Tray **title** and **body** | caller |
| **Priority** (`critical` / `high` / `normal`) | caller |
| **Event type** + app **data** payload | caller |
| Raw FCM device token in send requests | **never** — qcom looks it up |

Callers must **not** pass `fcm_token` on the send API. Register tokens via the device-token endpoint only.

---

## Architecture

```
┌──────────────────┐   PUT /api/v1/device-token (JWT)   ┌─────────────────────┐
│  BunzoApp        │ ─────────────────────────────────▶│                     │
│  driver-app      │                                   │  qcom               │
└──────────────────┘                                   │  token store + FCM  │
                                                       │                     │
┌──────────────────┐   POST /internal/v1/              │                     │
│  order-service   │   notifications/send             │                     │
│  (private VPC)   │ ─────────────────────────────────▶│                     │
└──────────────────┘                                   └─────────────────────┘

qcom internal code (assignment cron, trip service) calls the same Send logic in-process — no HTTP loopback.
```

**Network rule:** `/internal/v1/*` must **not** be exposed on the public ALB. Only services inside the VPC (e.g. order-service) should reach the internal send endpoint. No API key is required today — security is network isolation.

---

## Recipient types and IDs

| `recipient_type` | `recipient_id` | Used for |
|---|---|---|
| `driver` | qcom `de_id` (JWT `entity_id` for drivers) | Driver app |
| `customer` | qcom `user_id` (JWT `entity_id` for customers) | BunzoApp |
| `picker` | numeric picker id | Reserved for future central picker push |

**Customer rule:** `recipient_id` must be the qcom **`user_id`** returned from `POST /api/v1/auth/verify-otp` — the same value stored as `customer_id` on orders. Do not use phone number as `recipient_id`.

**Driver rule:** `recipient_id` must be **`de_id`**, not phone number.

---

## Priority

Three tiers control **FCM transport urgency** only (not copy):

| Priority | FCM transport | Typical use |
|---|---|---|
| `critical` | High (Android + APNS) | Order assigned to driver, time-sensitive ops |
| `high` | High | Important status updates (out for delivery, delivered) |
| `normal` | Normal | Promotional or low-urgency messages |

**Minimum priority enforcement:** some event types reject sends below a floor. Today:

| `event_type` | Minimum priority |
|---|---|
| `ORDER_ASSIGNED` | `critical` |

**Sound:** system default only — callers cannot set custom notification sounds.

---

## 1. Register / update device token (mobile apps)

Apps register FCM tokens after login and on token refresh. Identity comes from the JWT — **do not** send `recipient_type` or `recipient_id` in the body.

| | |
|---|---|
| **Method** | `PUT` |
| **Path** | `/api/v1/device-token` |
| **Auth** | `Authorization: Bearer <access_token>` |
| **Supported JWT** | `entity_type: "customer"` or `entity_type: "de"` |

### Request

```http
PUT /api/v1/device-token
Authorization: Bearer <access_token>
Content-Type: application/json

{
  "fcm_token": "<firebase-device-token>",
  "platform": "android"
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `fcm_token` | string | yes | Firebase device token. Send `""` to clear on logout. |
| `platform` | string | no | `"android"` or `"ios"`. Stored for debugging; not required in v1. |

### Response — success

```http
HTTP/1.1 200 OK

{"status":"ok"}
```

### Errors

| HTTP | Code | When |
|---|---|---|
| 401 | `UNAUTHORIZED` | Missing or invalid access token |
| 403 | `FORBIDDEN` | JWT `entity_type` is not `customer` or `de` (e.g. guest) |
| 400 | `INVALID_REQUEST` | Malformed JSON |
| 500 | `DEVICE_TOKEN_UPDATE_FAILED` | DynamoDB write failed |

### When to call

| Moment | Action |
|---|---|
| After login / verify-otp | `PUT` with current FCM token |
| FCM `onTokenRefresh` | `PUT` with new token |
| Logout | `PUT` with `"fcm_token": ""` |

### JWT → stored key mapping

| JWT `entity_type` | Stored `recipient_type` | Stored `recipient_id` |
|---|---|---|
| `customer` | `customer` | JWT `entity_id` (= `user_id`) |
| `de` | `driver` | JWT `entity_id` (= `de_id`) |

### Example — driver app

```bash
curl -X PUT "https://api.bunzodelivery.com/api/v1/device-token" \
  -H "Authorization: Bearer <de_access_token>" \
  -H "Content-Type: application/json" \
  -d '{"fcm_token":"eA3...","platform":"android"}'
```

### Example — customer app (BunzoApp)

```bash
curl -X PUT "https://api.bunzodelivery.com/api/v1/device-token" \
  -H "Authorization: Bearer <customer_access_token>" \
  -H "Content-Type: application/json" \
  -d '{"fcm_token":"eA3...","platform":"android"}'
```

---

## 2. Send notification (backend services)

Backend services send pushes by describing the recipient and message. qcom resolves the token and calls FCM.

| | |
|---|---|
| **Method** | `POST` |
| **Path** | `/internal/v1/notifications/send` |
| **Auth** | None (private network only) |
| **Base URL** | Internal qcom host (e.g. `http://<qcom-private-host>:8080`) — **not** the public API URL from the internet |

### Request body

```json
{
  "recipient_type": "customer",
  "recipient_id": "550e8400-e29b-41d4-a716-446655440000",
  "event_type": "ORDER_PACKED",
  "priority": "high",
  "title": "Order packed",
  "body": "Your order is packed and will ship soon.",
  "data": {
    "order_id": "ORD123456",
    "order_uuid": "abc-123-def"
  }
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `recipient_type` | string | yes | `driver`, `customer`, or `picker` |
| `recipient_id` | string | yes | Canonical id for that type (see table above) |
| `event_type` | string | yes | App routing key; also sent as FCM `data.type` |
| `priority` | string | yes | `critical`, `high`, or `normal` |
| `title` | string | yes | Tray notification title |
| `body` | string | yes | Tray notification body |
| `data` | object | no | String key-value pairs merged into FCM data payload |

### Response

Always **`202 Accepted`** (best-effort async semantics):

```json
{"status":"sent","message_id":"projects/.../messages/..."}
```

```json
{"status":"skipped","reason":"no_token"}
```

| `status` | Meaning |
|---|---|
| `sent` | FCM accepted the message |
| `skipped` | No send attempted — see `reason` |

| `reason` | Meaning |
|---|---|
| `no_token` | Recipient has not registered a device token |
| `push_disabled` | `FIREBASE_CREDENTIALS_B64` not configured on qcom |
| `token_lookup_failed` | DynamoDB read error |
| `send_failed` | FCM rejected the send (transient or unknown error) |
| *(validation message)* | Invalid request (bad priority, missing title, etc.) |

**Important:** `skipped` is **not an HTTP error**. Callers should log and continue — missing tokens are normal for users who denied notification permission or never opened the app after login.

### Example — order-service → customer (ORDER_PACKED)

```bash
curl -X POST "http://<qcom-internal-host>:8080/internal/v1/notifications/send" \
  -H "Content-Type: application/json" \
  -d '{
    "recipient_type": "customer",
    "recipient_id": "<order.customerId>",
    "event_type": "ORDER_PACKED",
    "priority": "high",
    "title": "Order packed",
    "body": "Your order is ready and will be dispatched soon.",
    "data": {
      "order_id": "ORD123456",
      "order_uuid": "abc-123-def"
    }
  }'
```

Use `order.customerId` from the order row — it must be the qcom `user_id`.

### Example — qcom → driver (ORDER_ASSIGNED)

Called in-process from qcom assignment cron (same JSON shape):

```json
{
  "recipient_type": "driver",
  "recipient_id": "DE-abc123",
  "event_type": "ORDER_ASSIGNED",
  "priority": "critical",
  "title": "New order!",
  "body": "Tap to view your trip.",
  "data": {
    "trip_id": "trip-uuid",
    "order_id": "ORD123456",
    "accept_deadline": "2026-06-21T08:00:00Z"
  }
}
```

---

## FCM payload delivered to the device

qcom sends a **notification + data** hybrid message:

| FCM field | Source |
|---|---|
| `notification.title` | request `title` |
| `notification.body` | request `body` |
| `data.type` | request `event_type` |
| `data.*` | request `data` (merged; caller keys must not overwrite `type`) |

**Android (driver, critical/high):** high transport priority, channel `order-alert-v3`, optional `tag` from `data.trip_id`.

**Android (customer, critical/high):** high transport priority, default channel.

**iOS (critical/high):** `apns-priority: 10`; `apns-collapse-id` set when `data.trip_id` is present.

Mobile apps should handle pushes by reading **`data.type`** and routing in-app (open screen, refresh state, etc.). Tray title/body are for the OS notification shade.

---

## Planned event types (v1 roadmap)

| Event | Sender | Recipient | Priority | Status |
|---|---|---|---|---|
| `ORDER_ASSIGNED` | qcom (assignment cron) | `driver` | `critical` | **Live** |
| `ORDER_PACKED` | order-service | `customer` | `high` | **Live** |
| `ORDER_OUT_FOR_DELIVERY` | qcom (trip service) | `customer` | `high` | **Live** |
| `ORDER_DELIVERED` | qcom (trip service) | `customer` | `high` | **Live** |

When adding a new `event_type`, coordinate with mobile teams so the app handles `data.type` correctly. If an event needs a minimum priority, add it to qcom's server-side registry.

---

## Integration checklist

### Mobile app engineer

- [ ] Obtain FCM token via Firebase SDK after notification permission granted
- [ ] `PUT /api/v1/device-token` after login and on token refresh
- [ ] Clear token on logout (`fcm_token: ""`)
- [ ] Handle foreground/background/killed notification taps using `data.type`
- [ ] Do **not** hardcode Firebase Admin credentials in the app

### Backend engineer (order-service)

- [ ] Set `QCOM_NOTIFICATION_ENABLED=true` and `QCOM_NOTIFICATION_BASE_URL` to the private qcom host
- [ ] Ensure order-service can reach qcom `/internal/v1/notifications/send` and qcom can reach order-service `/internal/v1/orders/{ref}/notification-target`
- [ ] On pick complete → `ORDER_PACKED` is sent automatically (no code changes per notification)
- [ ] Do **not** remove `FcmPickerNotificationService` — picker→picker push stays on order-service

### Backend engineer (new sender)

- [ ] Use internal qcom URL reachable from your service's VPC/security group
- [ ] Pass `recipient_type` + `recipient_id` — never raw FCM token
- [ ] Supply `title`, `body`, `event_type`, `priority`, and relevant `data`
- [ ] Treat `202` + `status: skipped` as non-fatal
- [ ] Fire send asynchronously (do not block critical user flows on push delivery)
- [ ] Remove direct Firebase SDK usage from your service once migrated

### Infra

- [ ] Block `/internal/v1/*` on the public ALB
- [ ] Ensure order-service security group can reach qcom on the internal port
- [ ] qcom must have `FIREBASE_CREDENTIALS_B64` set in SSM/env or pushes noop with `push_disabled`

---

## Environment variables (qcom server)

| Variable | Purpose |
|---|---|
| `FIREBASE_CREDENTIALS_B64` | Base64-encoded Firebase service account JSON. When empty, send returns `skipped` / `push_disabled`. |

---

## Deprecated (removed)

| Removed | Replacement |
|---|---|
| `POST /api/v1/de/fcm-token` | `PUT /api/v1/device-token` |
| `fcm_token` field on DE DynamoDB record | Central token store (`NOTIF!driver!{de_id}`) |
| Per-service Firebase Admin SDK | `POST /internal/v1/notifications/send` |

---

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Always `skipped` / `no_token` | App never called `PUT /api/v1/device-token`, or wrong `recipient_id` on send |
| Customer never gets push | `recipient_id` is phone instead of `user_id`, or BunzoApp token registration not wired |
| Driver never gets push | `recipient_id` is phone instead of `de_id`, or driver app still on old `/de/fcm-token` endpoint |
| Always `push_disabled` | Firebase credentials missing on qcom instance |
| Send rejected for priority | Event requires higher priority (e.g. `ORDER_ASSIGNED` needs `critical`) |
| Token stops working after reinstall | Expected — app must re-register via `PUT /device-token`; qcom clears invalid tokens automatically |

For server-side debugging, search qcom logs for `push notification sent`, `push skipped`, or `FCM token invalid`.
