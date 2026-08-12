# Rider route JWT auth audit (qcom)

Audit of `/api/v1/de/*` and `/api/v1/trip/*` route wiring in `cmd/server/main.go` as of the centralised JWT auth work.

## Middleware reference

| Middleware | Behaviour |
|---|---|
| `RequireDEAuth` | Valid bearer JWT with `entity_type == "de"` |
| `RequireAuth` | Valid bearer JWT (customer **or** de) |
| none | Public; no bearer required |

## `/api/v1/de/*` routes

| Route | Method | Middleware | OK? |
|---|---|---|---|
| `/api/v1/de/register` | POST | none (public by design) | intentional |
| `/api/v1/de/me` | GET | `RequireDEAuth` | yes |
| `/api/v1/de/duty/start` | POST | `RequireDEAuth` | yes |
| `/api/v1/de/duty/end` | POST | `RequireDEAuth` | yes |
| `/api/v1/de/trip` | GET | `RequireDEAuth` | yes |
| `/api/v1/de/referral` | GET | `RequireDEAuth` | yes |
| `/api/v1/de/earnings/summary` | GET | `RequireDEAuth` | yes |
| `/api/v1/de/earnings/disbursements` | GET | `RequireDEAuth` | yes |

`deProtected` subrouter (`api.PathPrefix("/de")`) applies `RequireDEAuth` to all routes except `/de/register`, which is registered on the parent `api` router before the subrouter.

## `/api/v1/trip/*` routes

| Route | Method | Middleware | OK? |
|---|---|---|---|
| `/api/v1/trip/{tripId}/task/{taskId}/status/update` | POST | `RequireDEAuth` | yes |
| `/api/v1/trip/{tripId}/accept` | POST | `RequireDEAuth` | yes |
| `/api/v1/trip/{tripId}/reject` | POST | `RequireDEAuth` | yes |
| `/api/v1/trip/{tripId}/verify-pickup` | POST | `RequireDEAuth` | yes |
| `/api/v1/trip/{tripId}/task/{taskId}/photo/presign` | POST | `RequireDEAuth` | yes |

`tripRoutes` subrouter (`api.PathPrefix("/trip")`) applies `RequireDEAuth` to every route in the table.

## Related rider-facing route

| Route | Method | Middleware | OK? |
|---|---|---|---|
| `/api/v1/device-token` | PUT | `RequireAuth` (customer or de) | acceptable |

Push notification token registration is shared between customer and driver apps; `RequireAuth` is intentional.

## Out of scope (not rider JWT routes)

- `POST /api/v1/admin/de/{phone}/cash-deposit` — `RequireAdminAuth`
- `POST /api/v1/admin/de/{deId}/disbursement` — `RequireAdminAuth`
- `POST /internal/v1/trips/*` — internal service-to-service (no JWT)

## Findings

No gaps. Every DE **write** route is behind `RequireDEAuth`; `/de/register` remains public. All trip progression routes are behind `RequireDEAuth`. No `cmd/server/main.go` changes required.
