# qcom

Go last-mile service for [Bunzo](https://bunzodelivery.com). Production hostname: `https://api.bunzodelivery.com`.

Module: `github.com/qcom/qcom`. Binary: `cmd/server` → `qcom-server`.

This is **not** the whole Bunzo backend.

## What this service owns

- Customer, rider (DE), and admin auth
- Serviceability (darkstore polygons, ETA) and darkstore CRUD
- Rider duty, trip assignment, and trip progression
- Customer order tracking (trip state + order-service payload)
- Earnings, disbursements, cash deposits, referrals
- Customer disputes
- Device tokens and FCM push
- Voice tokens / Vonage answer+event webhooks
- Uploads (presigned S3) and marketing QR redirects

Assignment is a 10s in-process cron (`internal/service/assignment_cron.go`). Each tick lists active darkstores, polls the Java **order-service** for `READY_FOR_DELIVERY` orders, creates trips, and assigns eligible DEs. A DynamoDB lock stops overlapping ticks across instances.

## What this service does not own

Catalog, cart, checkout, and payment capture (Airtel / MTN / card) are **not** in this repo. They live in the Java services (product-service, order-service).

qcom's only Java client is order-service, configured with `JAVA_ORDER_SERVICE_URL` (`internal/service/java_order_client.go`). Paths are prefixed `/order-service`.

Storage here is **DynamoDB + S3**. This process does not use Postgres or Redis.

## Stack

| | |
|---|---|
| Language | Go 1.25.8 (`go.mod`) |
| HTTP | gorilla/mux, `:PORT` (default `8080`) |
| Auth | Phone OTP + HS256 JWT (customer / DE); username+password JWT (admin) |
| OTP SMS | Twilio Verify (required at boot); Africa's Talking when configured (Zambian `+260` numbers) |
| Push | FCM via `FIREBASE_CREDENTIALS_B64` (no-op if unset) |
| Voice | Vonage (`VONAGE_VOICE_*`) |
| Maps | Google Maps (`GOOGLE_MAPS_API_KEY`) |
| Metrics | Prometheus on `127.0.0.1:METRICS_PORT` (default `2112`), not on the public port |

## Run locally

Prerequisites: Go 1.25.8, Docker + Docker Compose, AWS CLI (table/seed scripts), Twilio Verify credentials (the process **will not start** without them).

```bash
make setup          # DynamoDB :8000 + LocalStack S3 :4566, then scripts/create-table.sh
# export the required env vars (see below)
make build
make run            # ./bin/qcom-server
```

Equivalent: `go run ./cmd/server` or `go build -o ./bin/qcom-server ./cmd/server`.

`make docker-up` / `make docker-down` start/stop compose only. `make dev` is `setup` + `run`. `make help` lists the rest.

Local DynamoDB/S3 need dummy AWS credentials on the host (the SDK still signs requests):

```bash
export AWS_ACCESS_KEY_ID=dummy
export AWS_SECRET_ACCESS_KEY=dummy
export AWS_DEFAULT_REGION=us-east-1
```

There is no `.env` loader in the binary. Export vars in the shell. Compose does not inject them into `qcom-server`.

Optional after `make setup`:

```bash
./scripts/seed-darkstores.sh      # sample store polygons
./scripts/seed-home-page.sh       # PAGE#HOME for POST /api/v1/home
./scripts/seed-dispute-config.sh
```

Assignment, track enrichment, and dispute-against-order need a reachable order-service (`JAVA_ORDER_SERVICE_URL`, default `http://localhost:8081`). The HTTP API still boots without it.

## Environment variables

Loaded by `config.Load()` in `internal/config/config.go`, plus three bootstrap vars read in `cmd/server/main.go`. Empty string in the source means “no default”.

### Required to boot

| Variable | Notes |
|---|---|
| `JWT_SECRET_KEY` | HS256 secret; **min 32 bytes**. No default. |
| `TWILIO_ACCOUNT_SID` | No default. |
| `TWILIO_AUTH_TOKEN` | No default. |
| `TWILIO_VERIFY_SERVICE_SID` | No default. |

### Optional (defaults in code)

| Variable | Default | Notes |
|---|---|---|
| `PORT` | `8080` | Public HTTP listen port. |
| `METRICS_PORT` | `2112` | Loopback-only `/metrics`. |
| `DYNAMODB_ENDPOINT` | _(empty)_ | Set `http://localhost:8000` locally; empty = AWS. |
| `DYNAMODB_REGION` | `us-east-1` | |
| `DYNAMODB_TABLE_NAME` | `QComTable` | Single-table. |
| `JWT_ACCESS_EXPIRY` | `15m` | Go duration. |
| `JWT_REFRESH_EXPIRY` | `168h` | 7 days. |
| `OTP_LENGTH` | `6` | |
| `OTP_EXPIRY` | `10m` | |
| `OTP_MAX_ATTEMPTS` | `5` | |
| `S3_ENDPOINT` | _(empty)_ | Set `http://localhost:4566` for LocalStack; empty = AWS. |
| `S3_REGION` | `ap-southeast-2` | |
| `S3_BUCKET` | `printdrop-documents` | Default upload bucket. |
| `S3_TRIP_PHOTOS_BUCKET` | same as `S3_BUCKET` | |
| `S3_PRESIGN_EXPIRY_SECONDS` | `300` | |
| `S3_FORCE_PATH_STYLE` | `false` | Set `true` for LocalStack. |
| `GOOGLE_MAPS_API_KEY` | _(empty)_ | Geocode / ETA / distance. |
| `JAVA_ORDER_SERVICE_URL` | `http://localhost:8081` | Order-service base URL. |
| `AFRICASTALKING_USERNAME` | _(empty)_ | OTP SMS; unused if empty. |
| `AFRICASTALKING_API_KEY` | _(empty)_ | Unused if empty. |
| `AFRICASTALKING_BASE_URL` | `https://api.africastalking.com` | |
| `FIREBASE_CREDENTIALS_B64` | _(empty)_ | Base64 service-account JSON; FCM no-op if empty/invalid. |
| `DISPUTE_ELIGIBLE_ORDER_STATUSES` | `DELIVERED` | Comma-separated; uppercased. |
| `VONAGE_VOICE_APP_ID` | _(empty)_ | |
| `VONAGE_VOICE_PRIVATE_KEY` | _(empty)_ | Base64 private key. |
| `VONAGE_VOICE_SIGNATURE_SECRET` | _(empty)_ | Event webhook HMAC. |
| `SERVICEABILITY_BYPASS_USER_IDS` | _(empty)_ | Comma-separated JWT `entity_id`s. |
| `IS_TEST` / `IS_TRUE` | unset | Either equal to `true` skips the polygon check and uses the first active darkstore. |
| `ADMIN_BOOTSTRAP_USERNAME` | _(empty)_ | With password, creates the first admin if missing. |
| `ADMIN_BOOTSTRAP_PASSWORD` | _(empty)_ | Min 8 chars (same rule as admin create). |
| `ADMIN_BOOTSTRAP_NAME` | username | |

Production instances load `/app/.env` via systemd (`deploy/qcom.service`); `scripts/fetch-env.sh` writes that file from SSM `/qcom/prod/*`.

## Auth

**Customer and rider.** `POST /api/v1/auth/initiate-otp` then `POST /api/v1/auth/verify-otp` with an E.164 `phone_number` (`+[1-9]` + 1–14 digits). Response is an HS256 access token + refresh token (`Authorization: Bearer …`).

`X-App-Type: de` selects rider login (DE must already exist). Anything else is customer (get-or-create). Refresh: `POST /api/v1/auth/refresh`. Logout requires a valid access token.

**Admin.** `POST /api/v1/admin/login` with username/password. Token `entity_type` is `admin`. All `/api/v1/admin/*` except login use `RequireAdminAuth`.

**Guest.** `X-User-Category: guest` is accepted only on serviceability and reverse geocode (no JWT).

## HTTP surface

Registered in `setupRouter` in `cmd/server/main.go`. 80+ method+path pairs; grouped below. CORS/OPTIONS is on almost every route. Do not treat this as an OpenAPI spec.

### Public

| Method | Path | Auth |
|---|---|---|
| `GET` | `/health` | None |
| `GET` | `/q/{slug}` | None (marketing QR redirect) |
| `POST` | `/api/v1/auth/initiate-otp` | None |
| `POST` | `/api/v1/auth/verify-otp` | None |
| `POST` | `/api/v1/auth/refresh` | None |
| `POST` | `/api/v1/auth/logout` | Bearer |
| `POST` | `/api/v1/de/register` | None |
| `GET` | `/api/v1/stores/{storeId}/qr` | None (darkstore duty QR) |
| `PATCH` | `/api/v1/config/payout` | None (runtime payout config) |
| `POST` | `/api/v1/admin/login` | None |

### Webhooks (no JWT)

| Method | Path |
|---|---|
| `POST` | `/webhooks/voice/answer` |
| `POST` | `/webhooks/voice/event` |
| `POST` | `/webhooks/outbound-whatsapp-message-status` |
| `POST` | `/webhooks/inbound-whatsapp-message` |

Voice webhooks are Vonage (answer NCCO; event HMAC). WhatsApp handlers ack and log.

### Customer (`RequireAuth` unless noted)

| Method | Path |
|---|---|
| `GET` | `/api/v1/me` |
| `DELETE` | `/api/v1/users/me` |
| `POST` | `/api/v1/home` |
| `POST` | `/api/v1/uploads/url` |
| `GET` | `/api/v1/uploads/view-url` |
| `POST` | `/api/v1/print/files/upload-url` |
| `POST` | `/api/v1/serviceability` |
| `POST` | `/api/v1/geocode/reverse` |
| `GET` | `/api/v1/addresses/suggest` |
| `GET` `POST` | `/api/v1/addresses` |
| `GET` `PATCH` `DELETE` | `/api/v1/addresses/{id}` |
| `GET` | `/api/v1/orders/{orderId}/track` |
| `PUT` | `/api/v1/device-token` |
| `POST` | `/api/v1/voice/token` |
| `GET` | `/api/v1/disputes/dispositions` |
| `POST` | `/api/v1/disputes` |
| `GET` | `/api/v1/disputes/by-order` |
| `GET` | `/api/v1/disputes/{id}` |

Serviceability and reverse geocode: Bearer **or** `X-User-Category: guest`. Dispute routes: `RequireCustomerAuth`. Voice token: any valid customer/DE access token.

### Rider / DE (`RequireDEAuth`)

| Method | Path |
|---|---|
| `GET` | `/api/v1/de/me` |
| `POST` | `/api/v1/de/duty/start` |
| `POST` | `/api/v1/de/duty/end` |
| `GET` | `/api/v1/de/trip` |
| `GET` | `/api/v1/de/referral` |
| `GET` | `/api/v1/de/earnings/summary` |
| `GET` | `/api/v1/de/earnings/disbursements` |
| `POST` | `/api/v1/trip/{tripId}/accept` |
| `POST` | `/api/v1/trip/{tripId}/reject` |
| `POST` | `/api/v1/trip/{tripId}/verify-pickup` |
| `POST` | `/api/v1/trip/{tripId}/task/{taskId}/status/update` |
| `POST` | `/api/v1/trip/{tripId}/task/{taskId}/photo/presign` |

### Admin (`RequireAdminAuth` after login)

Under `/api/v1/admin/`:

- Account: `/me`, `/users`, `/users/{username}/password`
- SMS OTP routing: `/sms-otp-routing`
- Drop-reached config: `/config/drop-reached`
- Assign / reassign: `/assign`, `/trips/by-orders`, `/trips/{trip_id}/reassign-candidates`, `/trips/{trip_id}/reassign`
- Drivers: `/drivers`, `/drivers/{phone}` and sub-resources (earnings, disbursements, in-kind, referrals, cash ledger/collections, trips, presence, current trip, admin pickup/drop complete, assigned store)
- Darkstores: `/darkstores`, `/darkstores/{id}`, activate/deactivate
- Manual drop by order: `/orders/{orderId}/drop/preview`, `/orders/{orderId}/drop/complete`
- Cash / payouts: `/de/{phone}/cash-deposit`, `/de/{deId}/disbursement`
- Fare/reward rules: `/rules`, `/rules/{id}`, `/rules/{id}/versions`
- Disputes: `/disputes`, `/disputes/summary`, `/disputes/{id}`
- Marketing QR: `/qr/campaigns` and placements/analytics
- Driver doc presign: `/uploads/url`

### Internal (no JWT; intended as service-to-service)

| Method | Path |
|---|---|
| `POST` | `/internal/v1/notifications/send` |
| `POST` | `/internal/v1/trips/cancel-by-order` |
| `POST` | `/internal/v1/trips/payment/update` |
| `POST` | `/internal/v1/uploads/url` |
| `GET` | `/internal/v1/uploads/view-url` |

Called by order-service (cancel, payment method, picker uploads). Auth is network isolation, not a bearer token.

`GET /metrics` is **not** on the public router; it is bound to loopback only.

## Tests

```bash
make test                 # go test -v ./...
make test-coverage        # coverage.out + coverage.html
make test-upload          # go test -v -tags=integration -timeout 120s ./tests/integration/...  (Docker)
make test-integration     # scripts/integration-test.sh (build + local DynamoDB + curl)
make test-smoke           # live API; SMOKE_BASE_URL defaults to https://api.bunzodelivery.com
```

`go test ./...` skips files with `//go:build integration` or `//go:build smoke`. `make test-integration` boots the real binary, so the four required env vars must be set (the script does not set the Twilio ones).

`make fmt`, `make vet`, `make lint` (`golangci-lint`), `make check` (fmt+vet+lint).

## Production

systemd on EC2 behind an ALB (ASG `qcom-asg`). Unit: `deploy/qcom.service` (`ExecStart=/app/qcom/bin/qcom-server`, `EnvironmentFile=/app/.env`). No Kubernetes manifests in this repo.

```bash
make deploy               # scripts/deploy.sh — ASG rolling replace; needs .deploy.local.env
```

Health check used by deploy: `https://api.bunzodelivery.com/health`.

## Layout

```
cmd/server/          entrypoint + router
internal/config/     env loading
internal/handlers/   HTTP
internal/middleware/ CORS, auth, logging, metrics, trace
internal/service/    OTP, JWT, trips, assignment cron, FCM, Vonage, Java client
internal/repository/ DynamoDB
deploy/qcom.service  systemd unit
scripts/             table, seeds, deploy, SSM
tests/integration/   build tag integration
tests/smoke/         build tag smoke
docker-compose.yml   DynamoDB Local + LocalStack S3
```
