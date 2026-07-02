# Rider Presence & Exclusivity Tracking — Implementation Plan (v1)

## 1. Purpose

Enable a **presence-based rider retainer**: pay riders to stay at/near a POD (darkstore)
across the day — even when they aren't running orders — and verify that presence well
enough to justify paying for it.

**Scope of v1:** the system **records and visualizes** presence. It does **not** compute
pay. A human reads a per-day timeline + total online time on the admin dashboard and
decides pay offline.

**Approach:** scan-based sampling. On-duty riders must scan the store QR every 15 minutes,
and each scan is validated against a **tight per-store geofence** with basic anti-spoofing.
No continuous background GPS.

---

## 2. Core rules (agreed design)

1. **On-duty = scan the store QR every ~15 min at the pod, or get flipped offline.**
   Applies to idle (`eligible`) and post-delivery (`free`) riders alike. `busy` (on a trip)
   **pauses** the clock.
2. **Every scan is geofence-validated** against a tight per-store presence radius (~75 m),
   with a foreground one-shot GPS fix and mock-location rejection.
3. **Riders always re-scan from `offline`.** When the 15-min deadline passes, the server
   flips the rider `offline`; the rider then re-scans to return to `eligible`. There is no
   "heartbeat while eligible" — the existing `StartDuty` (`offline`/`free` → `eligible`) is
   reused.
4. **Push fires at the deadline** (when flipped offline): "You're offline — scan the store
   QR to get orders." No pre-deadline nudge.
5. **Online time = summed duration of `eligible` + `busy` + `free`**, measured by state
   transitions. A segment ends at the offline flip. Idle presence is credited; the trailing
   unverified time up to the deadline is knowingly included (see Accepted Risks).

### State / deadline matrix

| State | Carries 15-min deadline? | Deadline set when | On deadline | Online? |
|---|---|---|---|---|
| `offline` | no | — | — | no |
| `eligible` | yes | at the scan that made them eligible | → `offline` (`missed_scan`) | yes |
| `busy` | **paused** (cleared) | — | — | yes |
| `free` | yes | at delivery completion (`completed_at + 15m`) | → `offline` (`missed_scan`) | yes |

---

## 3. Data model changes (DynamoDB `QComTable`)

### 3.1 `DeliveryExecutive` (`qcom/internal/models/delivery_executive.go`)

Add:

```go
ScanDeadlineAt string `json:"scan_deadline_at,omitempty" dynamodbav:"scan_deadline_at,omitempty"` // RFC3339; when the next scan is due
LastScanLat    float64 `json:"last_scan_lat,omitempty" dynamodbav:"last_scan_lat,omitempty"`
LastScanLng    float64 `json:"last_scan_lng,omitempty" dynamodbav:"last_scan_lng,omitempty"`
LastScanAt     string `json:"last_scan_at,omitempty" dynamodbav:"last_scan_at,omitempty"`
```

Change the meaning of `duty_index_key`:

- **Today:** `"DE_ELIGIBLE#{storeId}"` only when `eligible`, cleared otherwise.
- **New:** `"DE_ONDUTY#{storeId}"` when `eligible` **or** `free`; cleared for `busy`/`offline`.
  `status` is already an attribute on the item and must be **projected into the
  `DEDutyIndex` GSI** so both consumers can filter.

> This is what makes `free` riders visible to the sweep. Both assignment and the sweep query
> the one `DE_ONDUTY#{store}` partition and filter by `status`.

### 3.2 `Darkstore` (`qcom/internal/models/darkstore.go`)

The darkstore already has `Latitude`/`Longitude` and is looked up by the same 3-char store
ID the QR uses. Add only:

```go
PresenceRadiusMeters float64 `json:"presence_radius_meters,omitempty" dynamodbav:"presence_radius_meters,omitempty"` // tight pod geofence; default 75
```

> **Do not** reuse `Polygon` for presence — that is the customer **serviceability** area
> (kilometres wide). Presence must be a tight radius around `Latitude`/`Longitude`.

Add a helper (haversine, since `PointInPolygon` is planar and the existing one is for large
zones):

```go
// DistanceMeters returns great-circle distance from the darkstore centre to (lat,lng).
func (d *Darkstore) DistanceMeters(lat, lng float64) float64 { /* haversine */ }

// WithinPresence reports whether a fix is inside the pod geofence, giving the
// rider the benefit of their GPS error circle.
func (d *Darkstore) WithinPresence(lat, lng, accuracyM float64) bool {
    r := d.PresenceRadiusMeters
    if r == 0 { r = 75 }
    return d.DistanceMeters(lat, lng)-accuracyM <= r
}
```

### 3.3 New: status-event log (powers the timeline + online-time)

One item per transition:

```
PK = DE!{phone}
SK = EVENT#{rfc3339_ts}#{ulid}
attrs: { from_state, to_state, reason, store_id, lat, lng, accuracy_m, ts }
```

`reason` ∈ `scan_start` | `scan_return` | `assigned` | `delivered` | `missed_scan` |
`ended_duty` | `cancelled`. Optional DynamoDB TTL (e.g. 400 days) for housekeeping.

---

## 4. Geofenced scan (augment `StartDuty`)

**Endpoint:** `POST /api/v1/de/duty/start` (existing;
`qcom/internal/handlers/de_handlers.go` → `DEService.StartDuty`).

### 4.1 Request (extended body)

```json
{
  "qr_code": "2212026070213",
  "lat": -15.4067,
  "lng": 28.2871,
  "accuracy_m": 18.5,
  "is_mocked": false
}
```

### 4.2 Validation order in `StartDuty`

1. Load DE. Reject if `busy` (unchanged). **Remove the `already_on_duty` hard error** is
   *not* needed — riders always scan from `offline`/`free`, so `eligible` won't occur; keep
   the guard as a safety no-op.
2. Cash-limit check (unchanged).
3. `ParseStoreID` + `ValidateQRCode` (unchanged — QR stays `storeId+hour`).
4. **Anti-spoof + geofence (new):**
   - Reject `INVALID_LOCATION` if `is_mocked == true`.
   - Reject `LOCATION_INACCURATE` if `accuracy_m > 150`.
   - Load darkstore for `storeID`; reject `OUTSIDE_GEOFENCE` if
     `!darkstore.WithinPresence(lat, lng, accuracy_m)`.
5. Transition `offline`/`free` → `eligible`; set `scan_deadline_at = now + 15m`,
   `duty_index_key = DE_ONDUTY#{store}`, stamp `last_scan_*`.
6. Append status-event (`scan_start` from offline, `scan_return` from free).

> Log every rejected scan (reason + coords + accuracy) for the fraud-review list.

---

## 5. Server-side sweep (auto-offline)

Piggyback the existing 10s assignment cron (`qcom/internal/service/assignment_cron.go`,
`tick()`), which already holds the distributed `cronLockRepo` lock — so the sweep runs on a
single instance without extra coordination.

New step in `tick()` (per store):

```
onDuty := deRepo.FindOnDutyByStore(store)         // GSI query on DE_ONDUTY#{store}
for de in onDuty:
    if de.status == busy or de.scan_deadline_at == "" { continue }   // paused
    if now >= de.scan_deadline_at:
        deRepo.MarkOfflineIfDeadlinePassed(de.phone, de.scan_deadline_at)  // conditional
        append status-event (missed_scan)
        notifier.Send(driver, "You're offline — scan the store QR to get orders")
```

### Repo additions (`qcom/internal/repository/de_repository.go`)

- `FindOnDutyByStore(store)` — Query `DEDutyIndex` on `DE_ONDUTY#{store}`.
- `FindEligibleByStoreFIFO` — repoint to `DE_ONDUTY#{store}` **and filter `status =
  eligible`** (keeps assignment FIFO behavior; free riders are skipped for assignment but
  visible to the sweep).
- `MarkOfflineIfDeadlinePassed(phone, expectedDeadline)` — `UpdateStatus`→offline guarded by
  `ConditionExpression: status IN (eligible, free) AND scan_deadline_at = :expected` so a
  rider who re-scanned between read and write is not wrongly offlined.

### Trip completion hook

Where the trip-completion transition sets the DE to `free`
(`qcom/internal/repository/trip_repository.go`), also set
`scan_deadline_at = now + 15m` and `duty_index_key = DE_ONDUTY#{store}`, and append a
`delivered` status-event. Where a trip is **assigned** (`eligible`→`busy`), clear
`scan_deadline_at` and `duty_index_key` (pause) and append `assigned`.

---

## 6. Online-time & timeline

- **Source of truth:** the status-event log (§3.3).
- **Online segment:** a contiguous run where state ∈ {`eligible`,`busy`,`free`}, from the
  entering event to the next `offline` event (`missed_scan` or `ended_duty`).
- **Total online time (day):** sum of segment durations, in **Zambia local time**
  (`timezone.ZambiaLocation()`), day boundary = Zambia midnight.
- Segments end at the offline flip (deadline). No "last verified event" trimming
  (deliberately reversed — see Accepted Risks).

**New endpoint:** `GET /api/v1/admin/drivers/{phone}/presence?date=YYYY-MM-DD` →

```json
{
  "date": "2026-07-02",
  "total_online_minutes": 412,
  "segments": [
    { "start": "08:03", "end": "08:18", "end_reason": "missed_scan", "store_id": "221" },
    { "start": "08:20", "end": "09:05", "end_reason": "ended_duty",  "store_id": "221" }
  ]
}
```

---

## 7. Admin dashboard

In the rider screen (`admin-dashboard/src/app/riders/[phone]/`), add a **Presence** tab:

- Date picker (default today, Zambia tz).
- Horizontal timeline of online segments + gaps; hover shows start/end/reason.
- **Total online time** for the day (headline number).
- Choppy timelines / long gaps / low online % = manual fraud signal.

---

## 8. Driver app (`driver-app`)

- Add `expo-location` with **"while in use"** (foreground) permission only. **No background
  location.**
- On the scanner screen (`app/qr-scanner.tsx`): **warm up** the GPS fix when the screen
  opens; on scan, capture `{lat, lng, accuracy, mocked}` and send with the existing
  duty-start call (`src/api/client.ts`).
- **Block go-online** if permission denied / location services off, with a clear
  "enable location to start your shift" message.
- Handle new rejections with a retry affordance: `OUTSIDE_GEOFENCE`
  ("move closer to the store"), `LOCATION_INACCURATE` ("move outdoors, try again"),
  `INVALID_LOCATION` ("disable mock location").
- Deadline push already flows through existing FCM; tapping it deep-links to the scanner.

---

## 9. Ops

- **Relocate the QR display outdoors** at the store entrance/forecourt (best GPS + naturally
  proves physical presence at the door).
- Seed `presence_radius_meters` (default 75) and verify `latitude`/`longitude` per darkstore
  (start with store `221`); tune radius per site after shadow data.

---

## 10. Phasing

| Phase | Deliverable |
|---|---|
| **0** | Data model: DE fields, `Darkstore.PresenceRadiusMeters` + `WithinPresence`, status-event log; `DEDutyIndex` projects `status`; seed store `221`. |
| **1** | Geofenced scan: extend duty-start body + anti-spoof/geofence checks; app sends location; block go-online without permission. |
| **2** | On-duty index broadening; deadline stamping (scan + completion); cron sweep auto-offline + conditional write + FCM. |
| **3** | Status-event logging on all transitions; `GET .../presence` endpoint + online-time computation. |
| **4** | Admin dashboard Presence tab. |
| **5 (deferred)** | Continuous-dwell upgrade (see §12). |

**Recommended:** run Phases 0–4 then **shadow for ~2 weeks** (record presence, don't pay on
it) to calibrate radius/interval before money is attached.

---

## 11. Consciously accepted risks (v1)

- **Manual pay** — system reports only; humans decide.
- **Disciplined multi-apper** who completes a competitor round-trip inside 15 min (bet:
  impractical for a full q-comm delivery).
- **Tail leakage** — up to ~15 unverified minutes credited per gap (offline is stamped at
  the deadline, not at actual departure).
- **Computable QR code** — `storeId+hour` is forgeable; a root-level GPS spoofer that
  bypasses the mock flag is **not** defended (bet: rider base is not sophisticated). No
  rotating server-signed token in v1.
- **Choppy/under-counted timeline** for a present idle rider who doesn't re-scan promptly
  after the "you're offline" push (no pre-deadline nudge).
- **No between-scan visibility** (foreground-only) — cannot detect absence between scans.

---

## 12. Explicit upgrade path (if abuse appears)

Piggyback `lat/lng` onto the existing 10s trip poll (`driver-app/src/services/pollingService.ts`)
and score **dwell** inside the presence geofence. This turns "online time" into
**verified at-pod minutes**, eliminating the tail leak and catching the disciplined
multi-apper the moment they leave the fence. Same geofence, same event log, same dashboard —
requires adding **background location** (Play Store review) and is the only structural
change. Keep this door closed for v1.
```
