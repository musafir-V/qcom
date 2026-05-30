# Serviceability API

The Serviceability API is the **single source of truth** for two questions the app asks constantly:

1. *"Can we deliver to this location?"*
2. *"If yes, which darkstore will fulfil it, roughly how long will it take, and what address should we show on screen?"*

A successful call returns everything the customer-facing UI needs to decide between rendering the home/catalog screen, rendering a *"sorry, not in your area"* screen, or asking the customer to refine their pin.

---

## Endpoint

| | |
|---|---|
| **Method** | `POST` |
| **Path** | `/api/v1/serviceability` |
| **Auth** | Required — Bearer access token (customer entity) |
| **Idempotent** | Yes — pure read; safe to retry |
| **Typical latency** | 80–300 ms (dominated by the Google Distance Matrix call) |

---

## When the app is supposed to call it

Serviceability is a **lightweight, latency-sensitive, frequently-called** endpoint. The app should call it at exactly these moments:

### Must-call moments

1. **App launch — home screen bootstrap.**
   As soon as the customer's GPS or last-known location resolves, call this endpoint **before** rendering the catalog. The response decides whether to show the catalog, the "not serviceable" screen, or a location-picker.

2. **Location picker — pin moved.**
   Each time the customer drags the map pin or taps a new spot, call this endpoint with the new coordinate. **Debounce by ~400–600 ms** so a continuous drag doesn't fan out 60 requests. The response drives the live banner at the bottom of the map ("Delivers in 12 min" vs. "Not deliverable here").

3. **Customer picks a saved address.**
   When the customer taps one of their saved addresses in the address-picker sheet, re-check serviceability for that address's coordinates. A darkstore zone could have shrunk since the address was saved.

4. **Checkout — final guard.**
   Immediately before transitioning to payment, re-call serviceability with the chosen delivery address's coordinates. This catches the edge case where the user's location was serviceable at app launch but the darkstore went inactive (manual override, end of shift) in the intervening minutes.

### Don't-call moments

- **Before GPS has settled.** Wait until the device reports accuracy better than ~50 m. A pre-fix coordinate will give a misleading answer and burn a Google Maps API call.
- **On every UI re-render.** Cache the last response keyed by `(lat rounded to 4 decimals, lng rounded to 4 decimals)` for the lifetime of the screen. ~11 m of resolution is enough — re-querying for sub-metre changes is waste.
- **Tight polling.** Don't poll on a timer to "refresh" serviceability. The polygons don't move that fast. If you need freshness, re-call on app foreground.

### Recommended client-side cache

Cache the response per location for **5 minutes** in memory. Invalidate on:
- App foregrounded after >5 minutes background
- User explicitly retries from a "not serviceable" screen
- Any 5xx response (don't cache failures)

---

## Request

```http
POST /api/v1/serviceability
Authorization: Bearer <access_token>
Content-Type: application/json

{
  "latitude":  37.7749,
  "longitude": -122.4194
}
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `latitude` | number | yes | Must be in `[-90, 90]`. Sent as a JSON number, not a string. |
| `longitude` | number | yes | Must be in `[-180, 180]`. |

> ⚠ Both fields are required. They're parsed as pointers internally so that omitting them is distinguishable from sending the legitimate coordinate `0, 0`. Send `null` and you'll get a `MISSING_FIELD` 400 — same as omitting.

---

## Response — happy path

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "data": {
    "serviceable": true,
    "darkstore_id": "ds_mumbai_powai_01",
    "resolved_address": {
      "address_line": "Flat 4B, Sapphire Heights",
      "tag": "home",
      "address_id": "addr_8d3e...",
      "source": "saved_address"
    },
    "eta_minutes": 12
  }
}
```

### Top-level fields (`data`)

| Field | Type | Always present? | Description |
|---|---|---|---|
| `serviceable` | boolean | **yes** | `true` iff the coordinate lies inside an active darkstore's polygon. The *only* field guaranteed to be present. |
| `darkstore_id` | string | only when `serviceable=true` | Stable ID of the fulfilling darkstore. The app should pass this through to subsequent catalog and cart calls — the same coordinate may fall in a different darkstore tomorrow. |
| `resolved_address` | object \| omitted | only when `serviceable=true` **and** an address could be resolved | See below. Omitted (not `null`) if neither saved-address matching nor reverse geocoding produced a result. |
| `eta_minutes` | integer \| omitted | only when `serviceable=true` **and** the ETA service responded | Estimated delivery time in whole minutes from the darkstore to the customer's coordinate. Omitted if the upstream Google Distance Matrix call failed. |

### `resolved_address` fields

| Field | Type | Always present in the object? | Description |
|---|---|---|---|
| `address_line` | string | yes | The full address as a single string. For saved addresses, this is `building_and_floor`, `address_line_1`, and `address_line_2` joined with `, ` (empty parts skipped) — e.g. `"Flat 4B, Sapphire Heights, MG Road, Near Test Park"`. For geocoded addresses, this is the formatted address from Google. |
| `tag` | string \| null | yes (null on geocoded) | The customer's label for the address (`home`, `work`, `other`, etc.). |
| `address_id` | string \| null | yes (null on geocoded) | UUID of the matched saved address. The app should prefer this for downstream operations. |
| `source` | string | yes | `"saved_address"` or `"geocoded"`. The app should style the row differently for each — saved addresses can be tapped to confirm, geocoded ones should prompt the user to save. |

> The response shape is intentionally minimal. If the app needs receiver name/phone or other structured address parts, fetch the full record by `address_id` via `GET /api/v1/addresses/{id}`.

---

## All the cases the app must handle

The response shape is deliberately sparse — fields are *omitted*, not nulled, when not applicable. Here is the full case matrix.

### Case 1 — Not serviceable

> Customer's coordinate is outside every active darkstore polygon.

```json
{ "data": { "serviceable": false } }
```

**App should:** Show "Not deliverable here. Move the pin or try a saved address." Don't render the catalog.

---

### Case 2 — Serviceable, saved address nearby, ETA available (full happy path)

> Customer is in a darkstore zone, has a previously-saved address within **50 m** of the current pin, and Google Distance Matrix responded.

```json
{
  "data": {
    "serviceable": true,
    "darkstore_id": "ds_mumbai_powai_01",
    "resolved_address": {
      "address_line": "Flat 4B, Sapphire Heights, MG Road, Near Test Park",
      "tag": "home",
      "address_id": "addr_8d3e...",
      "source": "saved_address"
    },
    "eta_minutes": 12
  }
}
```

**App should:** Render catalog; show "Delivery to **home** in **12 min**" at the top. Pre-fill `address_id` for the eventual checkout.

---

### Case 3 — Serviceable, saved address nearby, ETA failed

> Same as Case 2 but the Google call timed out / returned an error. The service logs a warning and returns the rest of the result without `eta_minutes`.

```json
{
  "data": {
    "serviceable": true,
    "darkstore_id": "ds_mumbai_powai_01",
    "resolved_address": {
      "address_line": "Flat 4B, Sapphire Heights, MG Road, Near Test Park",
      "tag": "home",
      "address_id": "addr_8d3e...",
      "source": "saved_address"
    }
  }
}
```

**App should:** Render catalog; show "Delivery to **home**" but suppress the ETA chip (or show "Calculating ETA…"). **Do not block the customer** — serviceability is the authoritative signal; ETA is best-effort.

---

### Case 4 — Serviceable, no saved address nearby, reverse geocode succeeded

> First-time user, or returning user in a new neighbourhood. Google reverse-geocoded the coordinate into a printable address.

```json
{
  "data": {
    "serviceable": true,
    "darkstore_id": "ds_mumbai_powai_01",
    "resolved_address": {
      "address_line": "Hiranandani Gardens, Powai, Mumbai 400076, India",
      "source": "geocoded"
    },
    "eta_minutes": 14
  }
}
```

**App should:** Render catalog; show the geocoded address with a "Save this address" affordance because `address_id` is absent.

---

### Case 5 — Serviceable, no saved address, geocode failed

> Rare: Google geocoding fails (quota, network blip). The service logs a warning and returns no `resolved_address` at all.

```json
{
  "data": {
    "serviceable": true,
    "darkstore_id": "ds_mumbai_powai_01",
    "eta_minutes": 14
  }
}
```

**App should:** Render catalog; prompt the customer to type / pick an address manually before checkout because we have nothing to show.

---

### Case 6 — Serviceable, both downstream calls failed

> Truly worst case for an in-zone customer. Both Google calls failed.

```json
{
  "data": {
    "serviceable": true,
    "darkstore_id": "ds_mumbai_powai_01"
  }
}
```

**App should:** Render catalog (we are serviceable, that's what counts). Hide ETA. Prompt for address entry before checkout.

---

### Case 7 — Multiple saved addresses within 50 m

> Customer has, say, both their home and a neighbour's place saved nearby.

The service returns **only the nearest one** (deterministic tie-break is by iteration order over the DynamoDB query result — effectively undefined for exact ties). The client only ever sees a single `resolved_address`.

**App should:** Trust the choice. If the user wants a different one, they can open the address picker, which has its own listing endpoint (`GET /api/v1/addresses`).

---

### Case 8 — Edge of a polygon

> Coordinate is on or very near a polygon boundary.

Ray-casting (`PointInPolygon` in `internal/models/darkstore.go`) is **not stable on the edge**: a coordinate exactly on an edge can flip between two adjacent calls. In practice, GPS noise (typically ±5 m) makes this self-resolving — but if the user manually pin-drops on a known edge, they may see flickering between Case 1 and Cases 2–6.

**App should:** If you receive different results for two near-identical coordinates within 1 second, prefer the most recent.

---

### Case 9 — Multiple overlapping darkstore polygons

> Should never happen in production data — polygons are documented as non-overlapping.

If they ever do overlap, the **first one returned by `DarkstoreRepository.ListActive`** wins (Go map iteration order in DynamoDB scans is undefined). This is a data-quality concern, not a client concern. The client always gets a single `darkstore_id`.

---

## Error responses

All errors follow the standard envelope:

```json
{ "error": { "code": "<CODE>", "message": "<human readable>" } }
```

| HTTP | Code | When | App should |
|---|---|---|---|
| 400 | `INVALID_REQUEST` | Body is not valid JSON | Treat as a bug; log and surface a generic error toast |
| 400 | `MISSING_FIELD` | `latitude` or `longitude` is null/missing | Same — this should never reach production from a sane client |
| 400 | `INVALID_COORDINATES` | lat/lng outside the valid Earth ranges | Same |
| 401 | `UNAUTHORIZED` | Missing/invalid/expired access token, or token's `entity_id` is empty | Trigger the refresh-token flow; if that also fails, send to login |
| 500 | `SERVICEABILITY_CHECK_FAILED` | DynamoDB unavailable, polygon parse error, etc. The error is logged server-side with `trace_id` | Retry once after ~500 ms with backoff. If still failing, fall back to a degraded UX (e.g. show last known cached serviceability) |

> Note: failure of the **ETA service** or the **reverse geocoder** does **not** produce an error response — the request still returns 200. Their absence is signalled by the omission of `eta_minutes` or `resolved_address` respectively. This is intentional: the app should be able to render *something* useful as long as serviceability itself is known.

---

## Behaviour — what actually happens server-side

Plain-English version of `ServiceabilityService.CheckServiceability`. Helpful for debugging:

1. **Fetch active darkstores.** Read all `DARKSTORE!*` rows where `is_active = true` from DynamoDB.
2. **Polygon test.** Iterate; the **first** darkstore whose polygon contains the point (ray-casting test) is the match. If none match, return `{ serviceable: false }` and stop.
3. **ETA (best-effort).** Call `ETAService.GetETA(ctx, matchedDarkstore, lat, lng)`. This goes through an H3-cell-keyed cache; on miss it hits Google Distance Matrix. If anything errors, log a warning and continue without `eta_minutes`.
4. **Saved-address resolution.** Load the caller's addresses (`AddressService.GetMyAddresses`). Walk them with the Haversine distance; track the nearest one within **50 m**. If found, populate `resolved_address` with `source = "saved_address"` and return.
5. **Geocoded-address fallback.** If no saved address was close enough, reverse-geocode the coordinate via the Google Geocoder. On success, populate `resolved_address` with `source = "geocoded"`. On failure, log a warning and omit the field.

The whole call is logged as one `op = "CheckServiceability"` entry with `duration_ms`, `serviceable`, and the `trace_id` propagated from the request — these are queryable in CloudWatch Logs Insights.

---

## Example — full request / response

```bash
curl -X POST https://api.qcom.example/api/v1/serviceability \
  -H "Authorization: Bearer eyJhbGciOiJI..." \
  -H "Content-Type: application/json" \
  -d '{"latitude": 19.1197, "longitude": 72.9081}'
```

```json
{
  "data": {
    "serviceable": true,
    "darkstore_id": "ds_mumbai_powai_01",
    "resolved_address": {
      "address_line": "Hiranandani Gardens, Powai",
      "tag": "home",
      "address_id": "addr_4f2a-...",
      "source": "saved_address"
    },
    "eta_minutes": 11
  }
}
```

---

## Open considerations

- **No throttling today.** A misbehaving client can drive a lot of Google Distance Matrix calls. Server-side rate-limit before scaling traffic.
- **`address_id` not propagated to cart.** The client must remember it; consider adding it to a session/cart object once cart endpoints exist.
- **No "near a darkstore but just outside" hint.** Today the response is binary. A future enhancement could include `nearest_darkstore_meters` for an "Almost there — try moving the pin" UX.
