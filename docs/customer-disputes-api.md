# Customer Disputes — Frontend API Contract

Backend for the customer **Order Dispute** feature (raise a dispute from the Order Details screen: pick a predefined reason, add a description, attach up to 3 photos, then view its status).

- **Service:** qcom
- **Base path:** `/api/v1`
- **Auth:** the customer's existing **Bearer access token** (the same token the print-upload flow already uses). All endpoints below require it.
  - Dispute endpoints are **customer-only** (`entity_type=customer`); a non-customer token → `403`.
- **Error envelope (all endpoints):**
  ```json
  { "error": { "code": "UPPER_SNAKE_CODE", "message": "human readable" } }
  ```
  Branch on `error.code`, not the message (5xx messages are intentionally generic).

---

## End-to-end flow

```
Order Details (delivered order)
        │
        ├─ GET /disputes/by-order?order_number=…           ← is there already a dispute?
        │     200 → show "View dispute"  (go to status screen)
        │     404 → show "Raise a dispute"
        │
   Raise a dispute
        │
        ├─ GET /disputes/dispositions                  ← list of reasons + per-reason rules
        │     user picks one → its flags decide if description/photos are required
        │
        ├─ for each photo (max 3):
        │     POST /uploads/url   (use_case=dispute_photo)   → { upload_url, object_key }
        │     PUT  <upload_url>   (raw image bytes)          → stash object_key
        │
        ├─ POST /disputes  { order_number, disposition_code, description, photo_keys[] }
        │     201 → success screen
        │     409 DISPUTE_ALREADY_OPEN → go to status screen
        │
   Status screen
        └─ GET /disputes/{id}  (or /disputes/by-order) → render reason, description, status
```

---

## 1. Get presigned upload URL (per photo)

`POST /api/v1/uploads/url`

Generic, use-case-driven upload endpoint. For dispute photos pass `use_case: "dispute_photo"`.

**Request**
```json
{
  "use_case": "dispute_photo",
  "file_name": "photo1.jpg",
  "file_type": "image/jpeg",
  "file_size": 482910
}
```

**Response `200`**
```json
{
  "file_id": "5f2c…",
  "upload_url": "https://<bucket>.s3…/disputes/<customerId>/<uuid>.jpg?X-Amz-Signature=…",
  "object_key": "disputes/<customerId>/<uuid>.jpg",
  "expires_in_seconds": 300,
  "max_file_size": 10485760
}
```

**Then upload the bytes directly to S3:**
```
PUT <upload_url>
Content-Type:   <the exact file_type you sent above>   ← must match
Content-Length: <file_size>
Body: raw file bytes
```
A `2xx` from the PUT means success. **Keep `object_key`** — that is what you submit with the dispute (not the URL).

**Rules for `dispute_photo`**
- Allowed `file_type`: `image/jpeg`, `image/png`, `image/heic`. Prefer sending **JPEG**.
- Max size: see `max_file_size` in the response (currently 10 MB).
- The presigned `upload_url` expires after `expires_in_seconds` (currently 300s) — request it right before uploading.
- Max **3** photos per dispute (enforced at dispute-create, not here).

**Errors**
| HTTP | code | when |
|------|------|------|
| 400 | `MISSING_FIELD` | `use_case` missing |
| 400 | `UNKNOWN_USE_CASE` | unknown `use_case` |
| 400 | `MIME_NOT_ALLOWED` | `file_type` not allowed for this use case |
| 400 | `FILE_TOO_LARGE` | `file_size` over the cap |
| 400 | `INVALID_REQUEST` | bad body / missing file fields |
| 403 | `ENTITY_TYPE_NOT_ALLOWED` | token type not allowed for this use case |
| 401 | `UNAUTHORIZED` | missing/invalid token |
| 500 | `PRESIGN_FAILED` | server-side; retry |

---

## 2. List dispositions (reasons)

`GET /api/v1/disputes/dispositions`

Returns the active, predefined dispute reasons, already ordered for display. **Render straight from this — do not hardcode reasons.** Each reason's flags tell you what the form must require.

**Response `200`**
```json
{
  "dispositions": [
    {
      "code": "ITEM_MISSING",
      "title": "An item was missing",
      "subtitle": "",
      "photos_required": false,
      "photo_min": 0,
      "description_required": false
    },
    {
      "code": "ITEM_DAMAGED",
      "title": "An item was damaged",
      "photos_required": true,
      "photo_min": 1,
      "description_required": false
    }
  ]
}
```

**Per-reason form rules**
- `description_required: true` → block submit until the description is non-empty.
- `photos_required: true` / `photo_min: N` → require at least `N` photos.
- Always allow up to 3 photos and an optional description regardless.

---

## 3. Create a dispute

`POST /api/v1/disputes`

**Request**
```json
{
  "order_number": "ORD-12345",
  "disposition_code": "ITEM_DAMAGED",
  "description": "The milk carton was crushed and leaking.",
  "photo_keys": ["disputes/<customerId>/<uuid>.jpg"]
}
```
- `order_number`, `disposition_code` — required.
- `description` — required only if the chosen disposition says so. When present: **min 10, max 500 characters**.
- `photo_keys` — the `object_key` values from step 1. Max 3. Each must be one of **your own** uploaded keys (`disputes/<yourCustomerId>/…`).

**Response `201`**
```json
{
  "dispute": {
    "dispute_id": "d_9f3…",
    "order_number": "ORD-12345",
    "disposition_code": "ITEM_DAMAGED",
    "description": "The milk carton was crushed and leaking.",
    "photo_keys": ["disputes/<customerId>/<uuid>.jpg"],
    "photo_urls": ["https://<bucket>.s3…/disputes/<customerId>/<uuid>.jpg?X-Amz-Signature=…"],
    "status": "OPEN",
    "created_at": "2026-06-22T10:31:00Z",
    "updated_at": "2026-06-22T10:31:00Z"
  }
}
```
New disputes are always `status: "OPEN"`. (`resolution_note` appears later, once an agent resolves it.)

**Errors**
| HTTP | code | meaning / UX |
|------|------|--------------|
| 400 | `MISSING_FIELD` | `order_number` or `disposition_code` missing |
| 404 | `ORDER_NOT_FOUND` | order doesn't exist |
| 409 | `ORDER_NOT_DISPUTABLE` | order isn't in a disputable (delivered) state |
| 400 | `DISPOSITION_NOT_FOUND` | unknown/inactive `disposition_code` |
| 400 | `DESCRIPTION_REQUIRED` | reason needs a description |
| 400 | `DESCRIPTION_TOO_SHORT` | < 10 chars |
| 400 | `DESCRIPTION_TOO_LONG` | > 500 chars |
| 400 | `TOO_MANY_PHOTOS` | more than 3 keys |
| 400 | `NOT_ENOUGH_PHOTOS` | fewer than the reason's `photo_min` |
| 400 | `INVALID_PHOTO_KEY` | a key isn't this customer's `disputes/<id>/…` key |
| 409 | `DISPUTE_ALREADY_OPEN` | a dispute already exists for this order → route to status screen |
| 401 | `UNAUTHORIZED` | missing/invalid token |

---

## 4. Get a dispute by order (drives the Order Details button)

`GET /api/v1/disputes/by-order?order_number=ORD-12345`

Returns the latest dispute for the order. Use it to decide whether Order Details shows **"Raise a dispute"** (`404`) or **"View dispute"** (`200`).

**Response `200`** — `{ "dispute": { …same shape as §3… } }`

**Errors**
| HTTP | code |
|------|------|
| 400 | `MISSING_FIELD` (no `order_number`) |
| 404 | `DISPUTE_NOT_FOUND` (none for this order) |
| 403 | `FORBIDDEN` (not your order's dispute) |

---

## 5. Get a dispute by id (status screen)

`GET /api/v1/disputes/{id}`

**Response `200`** — `{ "dispute": { …same shape as §3… } }`

Each `photo_urls[i]` is a short-lived presigned GET URL for `photo_keys[i]`. URLs expire after `expires_in_seconds` (same as upload presign, currently 300s). Re-fetch the dispute (or call §1b) to refresh expired URLs.

**Errors:** `404 DISPUTE_NOT_FOUND`, `403 FORBIDDEN`.

`status` is one of `OPEN`, `UNDER_REVIEW`, `RESOLVED`, `REJECTED`. In this release only `OPEN` is produced (resolution is handled by an internal tool that ships later). Render a status pill; show `resolution_note` if/when present.

---

## 1b. Get presigned view URL (refresh a single photo)

`GET /api/v1/uploads/view-url?use_case=dispute_photo&object_key=disputes/<customerId>/<uuid>.jpg`

Use when you already have an `object_key` and need a fresh view URL (e.g. after the URLs from §4/§5 expired).

**Response `200`**
```json
{
  "view_url": "https://<bucket>.s3…/disputes/<customerId>/<uuid>.jpg?X-Amz-Signature=…",
  "object_key": "disputes/<customerId>/<uuid>.jpg",
  "expires_in_seconds": 300
}
```

**Errors**
| HTTP | code | when |
|------|------|------|
| 400 | `MISSING_FIELD` | `use_case` or `object_key` missing |
| 400 | `INVALID_OBJECT_KEY` | key isn't this customer's `disputes/<id>/…` key |
| 400 | `UNKNOWN_USE_CASE` | unknown `use_case` |
| 401 | `UNAUTHORIZED` | missing/invalid token |
| 500 | `PRESIGN_FAILED` | server-side; retry |

---

## Business rules the UI should respect

1. **Only show the dispute entry point on delivered orders.** Non-delivered orders return `409 ORDER_NOT_DISPUTABLE`; don't lead the user into a dead end.
2. **One dispute per order.** Once one exists, flip the button to "View dispute". A second create returns `409 DISPUTE_ALREADY_OPEN`. (Note for this release: there is no customer "re-raise" — a resolved dispute does not re-open the ability to raise another, pending the internal resolution tool.)
3. **Upload before submit.** Photos go to S3 first (§1), then you submit only their `object_key`s in §3. Don't send image bytes to `/disputes`.
4. **Match the PUT `Content-Type`** to the `file_type` you presigned with, or S3 rejects the upload.
5. **Show stored photos via `photo_urls`.** GET dispute responses include presigned view URLs alongside `photo_keys`. Use `photo_urls[i]` in `<Image source={{ uri }} />`. If a URL expires (~300s), re-fetch the dispute or call §1b.

---

## Quick reference

| # | Method | Path | Auth | Purpose |
|---|--------|------|------|---------|
| 1 | POST | `/api/v1/uploads/url` | customer | presign a photo upload (`use_case=dispute_photo`) |
| 1b | GET | `/api/v1/uploads/view-url` | customer | presign a photo view URL (`use_case=dispute_photo`) |
| 2 | GET | `/api/v1/disputes/dispositions` | customer | list dispute reasons + rules |
| 3 | POST | `/api/v1/disputes` | customer | create a dispute |
| 4 | GET | `/api/v1/disputes/by-order?order_number=` | customer | latest dispute for an order |
| 5 | GET | `/api/v1/disputes/{id}` | customer | one dispute (status) |

*Reference implementation of the presign + PUT pattern already exists in the app: `BunzoApp/src/services/printService.ts` (`getPrintUploadUrl` / `uploadFileToS3`). The dispute photo upload is the same flow with `use_case: "dispute_photo"`.*
