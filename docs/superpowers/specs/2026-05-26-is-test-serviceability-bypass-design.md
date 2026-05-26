# IS_TEST Serviceability Bypass — Design

## Problem

The serviceability check in `internal/service/serviceability_service.go` runs a
point-in-polygon test against each active darkstore. In test environments we
often want to exercise the downstream serviceable path (ETA, address resolution,
geocoding) without setting up real darkstore polygons that cover the test
coordinates.

We need an env-var-controlled bypass so that when running in a test environment,
any coordinate is treated as serviceable.

## Goal

When the `IS_TEST` env var is set to the string `"true"`, `CheckServiceability`
skips the polygon containment check and treats the coordinate as serviceable,
attached to the first active darkstore.

Non-test environments are unaffected — `IS_TEST` unset, empty, or any value
other than `"true"` keeps today's behavior.

## Changes

### 1. `internal/config/config.go`

Add a top-level `IsTest bool` field on `Config`, loaded from the `IS_TEST` env
var using the existing `getEnv` helper. The truthy rule matches the existing
`S3_FORCE_PATH_STYLE` pattern: strictly the string `"true"`.

```go
type Config struct {
    Server   ServerConfig
    DynamoDB DynamoDBConfig
    JWT      JWTConfig
    OTP      OTPConfig
    S3       S3Config
    Google   GoogleConfig
    IsTest   bool
}
```

In `Load()`:

```go
IsTest: getEnv("IS_TEST", "false") == "true",
```

### 2. `internal/service/serviceability_service.go`

Add an `isTest bool` field on `ServiceabilityService`, accepted via the
constructor.

```go
type ServiceabilityService struct {
    darkstoreRepo  *repository.DarkstoreRepository
    addressService *AddressService
    geocoder       Geocoder
    etaService     ETAProvider
    logger         *logrus.Logger
    isTest         bool
}

func NewServiceabilityService(
    darkstoreRepo *repository.DarkstoreRepository,
    addressService *AddressService,
    geocoder Geocoder,
    etaService ETAProvider,
    logger *logrus.Logger,
    isTest bool,
) *ServiceabilityService { ... }
```

In `CheckServiceability`, replace the polygon match block (lines 67-77) with:

```go
var matched *models.Darkstore
if s.isTest {
    if len(darkstores) == 0 {
        return &ServiceabilityResult{Serviceable: false}, nil
    }
    matched = &darkstores[0]
} else {
    for i := range darkstores {
        if darkstores[i].Contains(lat, lng) {
            matched = &darkstores[i]
            break
        }
    }
    if matched == nil {
        return &ServiceabilityResult{Serviceable: false}, nil
    }
}
```

Everything below this block — ETA, saved-address resolution, geocoding fallback
— runs unchanged on both paths.

### 3. `cmd/server/main.go`

Pass `cfg.IsTest` into `NewServiceabilityService(...)` at the call site.

## Out of Scope

- New tests. Existing integration tests in
  `tests/integration/serviceability_api_test.go` already exercise the
  serviceable path; they don't need to change.
- Handler-level changes. The response shape (`ServiceabilityResult`) is
  identical on both paths.
- Bypassing other checks (auth, rate-limiting, etc.). Only the polygon check
  is bypassed.

## Behavior Matrix

| `IS_TEST` value | Active darkstores | Result                                          |
| --------------- | ----------------- | ----------------------------------------------- |
| unset / `""`    | any               | today's behavior (polygon match)                |
| `"true"`        | empty list        | `Serviceable: false`                            |
| `"true"`        | non-empty         | `Serviceable: true`, matched to `darkstores[0]` |
| `"1"`, `"yes"`  | any               | treated as not-set; today's behavior            |
