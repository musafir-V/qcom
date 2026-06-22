# Frontend handoff: Serviceability operating hours

**Status:** Live in production (`POST /api/v1/serviceability`)  
**Backend commit:** `feat(qcom): add store operating hours to serviceability API`  
**Audience:** BunzoApp (React Native) frontend developers

---

## Summary

The serviceability API now distinguishes **why** a location is unserviceable and, when the store is **closed for the day**, returns enough info to show *“We’re closed — opens at 7:00 AM”* instead of the generic *“We don’t deliver here”* message.

No request changes. Same endpoint, same auth, same `{ latitude, longitude }` body.

### What changed

| Before | After |
|---|---|
| Unserviceable = `{ serviceable: false }` only | Unserviceable includes `reason` (`outside_delivery_zone` \| `store_inactive` \| `store_closed`) |
| No store hours in response | `operating_hours` + `is_operational` when a darkstore polygon matches and hours are valid |
| Closed store looked like out-of-zone | `store_closed` includes `next_opens_at` (RFC3339, Zambia time) |

### What did **not** change

- Request shape unchanged
- `serviceable: true` responses still include `darkstore_id`, `eta_minutes`, `resolved_address` as before
- Errors (400/401/500) unchanged
- ETA / geocode graceful-failure behaviour unchanged

---

## New response fields

All fields live under `data` in the JSON envelope.

| Field | Type | When present | Notes |
|---|---|---|---|
| `reason` | `string` | `serviceable === false` | `outside_delivery_zone` \| `store_inactive` \| `store_closed` |
| `is_operational` | `boolean` | Polygon match + valid hours | `true` when open, `false` when `store_closed`. Omitted for out-of-zone / inactive. |
| `operating_hours` | `object` | Polygon match + valid hours | Daily schedule (see below). Omitted for out-of-zone / inactive. |
| `next_opens_at` | `string` | `reason === "store_closed"` | Next opening instant, RFC3339 with offset (e.g. `2026-06-22T07:00:00+02:00`). **Use this for user-facing copy** — do not compute opening time on the client. |

### `operating_hours` object

```json
{
  "opens_at": "07:00",
  "closes_at": "23:00",
  "timezone": "Africa/Lusaka"
}
```

- Times are **24-hour `HH:MM`** in **Zambia time** (`Africa/Lusaka`, UTC+2).
- `closes_at` is **exclusive**: store is open while `now >= opens_at && now < closes_at`.
  - Example: `07:00`–`23:00` → open from 7:00 AM through **10:59 PM**; closed from 11:00 PM.
- Current production darkstore hours: **7:00 AM – 11:00 PM** Zambia time.

---

## Response cases (implement all three unserviceable UX paths)

Fields are **omitted** when not applicable (never `null`).

### 1. Outside delivery zone

Customer pin is outside every darkstore polygon.

```json
{
  "data": {
    "serviceable": false,
    "reason": "outside_delivery_zone"
  }
}
```

**UI**

- **Title:** keep existing — *“Sorry! We don’t deliver here”*
- **Subtitle:** *“Move the pin or try a saved address.”*
- **CTA:** *Change Location*
- Do **not** show operating hours.

---

### 2. Store inactive

Customer is **inside** a polygon, but the store is manually turned off or misconfigured.

```json
{
  "data": {
    "serviceable": false,
    "reason": "store_inactive",
    "darkstore_id": "221"
  }
}
```

**UI**

- **Title suggestion:** *“Bunzo is temporarily unavailable”*
- **Subtitle:** generic — *“Please check back later.”*
- Do **not** show `operating_hours` or `next_opens_at`.

---

### 3. Store closed (in zone, outside hours)

Customer is in the delivery zone but the store is outside its daily window.

```json
{
  "data": {
    "serviceable": false,
    "reason": "store_closed",
    "darkstore_id": "221",
    "is_operational": false,
    "operating_hours": {
      "opens_at": "07:00",
      "closes_at": "23:00",
      "timezone": "Africa/Lusaka"
    },
    "next_opens_at": "2026-06-22T07:00:00+02:00"
  }
}
```

**UI**

- **Title suggestion:** *“We’re closed right now”*
- **Subtitle:** format `next_opens_at` for display, e.g.:
  - Same calendar day: *“Opens today at 7:00 AM”*
  - Next day: *“Opens tomorrow at 7:00 AM”*
  - Later: *“Opens Mon, 7:00 AM”*
- Optional secondary line from `operating_hours`: *“Open daily 7:00 AM – 11:00 PM”*
- **CTA:** *Change Location* (optional) or no CTA — product call.
- Still treat as **unserviceable** for catalog / cart / tabs (`serviceable === false`).

**Formatting `next_opens_at`**

```typescript
// Parse server value; display in user's locale or fixed en-ZM as product prefers.
const opens = new Date(data.next_opens_at);

function formatNextOpen(iso: string): string {
  const dt = new Date(iso);
  const now = new Date();
  const sameDay =
    dt.getFullYear() === now.getFullYear() &&
    dt.getMonth() === now.getMonth() &&
    dt.getDate() === now.getDate();
  const time = dt.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });
  if (sameDay) return `Opens today at ${time}`;
  // compare tomorrow, etc.
  return `Opens at ${time}`;
}
```

Prefer **`next_opens_at`** over parsing `operating_hours.opens_at` — the server already handles “today vs tomorrow”.

---

### 4. Serviceable (happy path)

Unchanged behaviour, plus two new optional fields:

```json
{
  "data": {
    "serviceable": true,
    "darkstore_id": "221",
    "is_operational": true,
    "operating_hours": {
      "opens_at": "07:00",
      "closes_at": "23:00",
      "timezone": "Africa/Lusaka"
    },
    "eta_minutes": 12,
    "resolved_address": { "...": "..." }
  }
}
```

**UI**

- No change required for v1 unless you want to show hours in settings/footer.
- On **app foreground** / checkout re-check: if a previously serviceable session returns `store_closed`, flip to the closed-store wall immediately.

---

## TypeScript types (suggested)

Update `BunzoApp/src/services/serviceabilityService.ts`:

```typescript
export type ServiceabilityReason =
  | 'outside_delivery_zone'
  | 'store_inactive'
  | 'store_closed';

export interface OperatingHours {
  opensAt: string;   // "07:00"
  closesAt: string;  // "23:00"
  timezone: string;  // "Africa/Lusaka"
}

export interface ServiceabilityResult {
  serviceable: boolean;
  reason?: ServiceabilityReason;
  darkstoreId?: string;
  isOperational?: boolean;
  operatingHours?: OperatingHours;
  nextOpensAt?: string; // ISO / RFC3339
  resolvedAddress?: ResolvedAddress;
  etaMinutes?: number;
}
```

Map snake_case in `callOnce`:

```typescript
reason: data.reason,
isOperational: data.is_operational,
operatingHours: data.operating_hours
  ? {
      opensAt: data.operating_hours.opens_at,
      closesAt: data.operating_hours.closes_at,
      timezone: data.operating_hours.timezone,
    }
  : undefined,
nextOpensAt: data.next_opens_at,
```

---

## Suggested BunzoApp touchpoints

| File | Change |
|---|---|
| `src/services/serviceabilityService.ts` | Parse + cache new fields |
| `src/features/location/locationSlice.ts` | Optionally store `reason`, `nextOpensAt` for UI (or pass via serviceability cache) |
| `src/components/UnserviceableState.tsx` | Accept `title` / `message` per reason; today only `message` is customisable |
| `src/screens/HomeScreen.tsx` | Pass reason-specific copy into `UnserviceableState` |
| `src/screens/CategoriesScreen.tsx` | Same as Home |
| `src/hooks/useUnserviceable.ts` | Gate stays `serviceable === false`; optionally expose `reason` |
| `src/components/LocationSearchBottomSheet.tsx` | Map pin banner: “Closed — opens 7 AM” vs “Not deliverable here” |
| `src/screens/CartScreen.tsx` | Checkout guard: block with closed-store message if re-check returns `store_closed` |

### Existing gate logic (keep as-is)

```typescript
// useUnserviceable.ts — still correct
serviceable === false → 'unserviceable'  // includes store_closed AND outside_delivery_zone
serviceable === true  → 'serviceable'
null                  → 'checking'
```

Do **not** derive `serviceable` from `is_operational` on the client. Trust `serviceable` from the API.

Do **not** compute `is_operational` locally from `operating_hours` — server clock is authoritative.

---

## Decision tree (client)

```
serviceable === true
  → show app (catalog, tabs, cart)

serviceable === false
  → hide catalog / tabs (existing unserviceable wall)
  → switch on reason:
      outside_delivery_zone → "don't deliver here"
      store_inactive        → "temporarily unavailable"
      store_closed          → "we're closed" + format(next_opens_at)
      (missing reason)      → fallback to generic unserviceable copy (legacy-safe)
```

---

## QA checklist

- [ ] Pin **outside** polygon → `outside_delivery_zone`, generic no-delivery copy
- [ ] Pin **inside** polygon during open hours → `serviceable: true`, catalog loads
- [ ] Pin **inside** polygon outside hours → `store_closed`, closed copy with `next_opens_at`
- [ ] App **foreground** after store closes → re-call serviceability, wall appears without relaunch
- [ ] **Cart Proceed** re-check returns `store_closed` → block checkout with closed message
- [ ] Location picker live banner shows distinct text for closed vs out-of-zone
- [ ] Guest user path still works (no regression on auth / geocoded address)
- [ ] Cached serviceability result includes new fields after first fetch

### Manual test coordinates

Ask backend for current polygon test coords. Example from dev seeds (may differ in prod):

- Inside zone: `latitude: 12.975, longitude: 77.640`
- Outside zone: `latitude: 13.5, longitude: 77.6`

To test `store_closed` in prod, call the API outside **07:00–23:00 Zambia time** with an in-zone coordinate.

---

## API reference

Full backend spec (including error cases and server-side logic): [`../SERVICEABILITY_API.md`](../SERVICEABILITY_API.md)

**Endpoint:** `POST https://api.bunzodelivery.com/api/v1/serviceability`  
**Auth:** `Authorization: Bearer <access_token>` (guest: `X-User-Category: guest`)

---

## Questions?

Contact backend if:

- `store_closed` missing `next_opens_at` (bug)
- In-zone customer gets `outside_delivery_zone` (polygon / data issue)
- `operating_hours` missing on `serviceable: true` for an active store (darkstore missing hours in DynamoDB)
