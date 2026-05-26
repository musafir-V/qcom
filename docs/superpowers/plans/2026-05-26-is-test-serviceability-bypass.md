# IS_TEST Serviceability Bypass Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an `IS_TEST` env-var-controlled bypass so `CheckServiceability` skips the point-in-polygon check and treats any coordinate as serviceable against the first active darkstore.

**Architecture:** Add a top-level `IsTest bool` on `Config` (loaded from `IS_TEST` strictly equal to `"true"`), thread it into `ServiceabilityService` via the constructor, and gate the polygon loop behind it. Downstream ETA / address-resolution logic stays untouched on both paths.

**Tech Stack:** Go 1.x, `logrus`, project-local `config`/`service`/`repository`/`models` packages.

**Spec:** [docs/superpowers/specs/2026-05-26-is-test-serviceability-bypass-design.md](../specs/2026-05-26-is-test-serviceability-bypass-design.md)

---

### Task 1: Add `IsTest` field to Config

**Files:**
- Modify: `internal/config/config.go:10-17` (struct), `internal/config/config.go:55-98` (Load)

- [ ] **Step 1: Add `IsTest bool` to the `Config` struct**

Edit `internal/config/config.go`. Change:

```go
type Config struct {
	Server   ServerConfig
	DynamoDB DynamoDBConfig
	JWT      JWTConfig
	OTP      OTPConfig
	S3       S3Config
	Google   GoogleConfig
}
```

to:

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

- [ ] **Step 2: Populate `IsTest` in `Load()`**

In `internal/config/config.go`, inside the `cfg := &Config{...}` literal in `Load()`, add a new field after the `Google` block. The final literal should look like:

```go
cfg := &Config{
    Server: ServerConfig{
        Port:         getEnv("PORT", "8080"),
        ReadTimeout:  15 * time.Second,
        WriteTimeout: 15 * time.Second,
    },
    DynamoDB: DynamoDBConfig{
        Endpoint:  getEnv("DYNAMODB_ENDPOINT", ""),
        Region:    getEnv("DYNAMODB_REGION", "us-east-1"),
        TableName: getEnv("DYNAMODB_TABLE_NAME", "QComTable"),
    },
    JWT: JWTConfig{
        SecretKey:     getEnv("JWT_SECRET_KEY", ""),
        AccessExpiry:  getEnvAsDuration("JWT_ACCESS_EXPIRY", 15*time.Minute),
        RefreshExpiry: getEnvAsDuration("JWT_REFRESH_EXPIRY", 7*24*time.Hour),
    },
    OTP: OTPConfig{
        Length:      getEnvAsInt("OTP_LENGTH", 6),
        Expiry:      getEnvAsDuration("OTP_EXPIRY", 10*time.Minute),
        MaxAttempts: getEnvAsInt("OTP_MAX_ATTEMPTS", 5),
    },
    S3: S3Config{
        Endpoint:             getEnv("S3_ENDPOINT", ""),
        Region:               getEnv("S3_REGION", "ap-southeast-2"),
        Bucket:               getEnv("S3_BUCKET", "printdrop-documents"),
        PresignExpirySeconds: getEnvAsInt("S3_PRESIGN_EXPIRY_SECONDS", 300),
        ForcePathStyle:       getEnv("S3_FORCE_PATH_STYLE", "false") == "true",
    },
    Google: GoogleConfig{
        MapsAPIKey: getEnv("GOOGLE_MAPS_API_KEY", ""),
    },
    IsTest: getEnv("IS_TEST", "false") == "true",
}
```

The only new line is `IsTest: getEnv("IS_TEST", "false") == "true",` — everything else is unchanged.

- [ ] **Step 3: Verify config compiles**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go build ./internal/config/...`
Expected: exits 0, no output.

- [ ] **Step 4: Commit**

```bash
cd /Users/shivangawasthi/bunzo/qcom
git add internal/config/config.go
git commit -m "config: add IsTest flag from IS_TEST env var"
```

---

### Task 2: Gate polygon check on `isTest` in `ServiceabilityService`

**Files:**
- Modify: `internal/service/serviceability_service.go:34-56` (struct + constructor), `internal/service/serviceability_service.go:60-77` (CheckServiceability polygon block)

- [ ] **Step 1: Add `isTest` field to the service struct**

In `internal/service/serviceability_service.go`, change:

```go
type ServiceabilityService struct {
	darkstoreRepo  *repository.DarkstoreRepository
	addressService *AddressService
	geocoder       Geocoder
	etaService     ETAProvider
	logger         *logrus.Logger
}
```

to:

```go
type ServiceabilityService struct {
	darkstoreRepo  *repository.DarkstoreRepository
	addressService *AddressService
	geocoder       Geocoder
	etaService     ETAProvider
	logger         *logrus.Logger
	isTest         bool
}
```

- [ ] **Step 2: Add `isTest` parameter to the constructor**

In the same file, change:

```go
func NewServiceabilityService(
	darkstoreRepo *repository.DarkstoreRepository,
	addressService *AddressService,
	geocoder Geocoder,
	etaService ETAProvider,
	logger *logrus.Logger,
) *ServiceabilityService {
	return &ServiceabilityService{
		darkstoreRepo:  darkstoreRepo,
		addressService: addressService,
		geocoder:       geocoder,
		etaService:     etaService,
		logger:         logger,
	}
}
```

to:

```go
func NewServiceabilityService(
	darkstoreRepo *repository.DarkstoreRepository,
	addressService *AddressService,
	geocoder Geocoder,
	etaService ETAProvider,
	logger *logrus.Logger,
	isTest bool,
) *ServiceabilityService {
	return &ServiceabilityService{
		darkstoreRepo:  darkstoreRepo,
		addressService: addressService,
		geocoder:       geocoder,
		etaService:     etaService,
		logger:         logger,
		isTest:         isTest,
	}
}
```

- [ ] **Step 3: Gate the polygon match block on `s.isTest`**

In `CheckServiceability`, change this block (currently lines 66-77):

```go
	// Polygons are non-overlapping, so the first containing polygon wins.
	var matched *models.Darkstore
	for i := range darkstores {
		if darkstores[i].Contains(lat, lng) {
			matched = &darkstores[i]
			break
		}
	}

	if matched == nil {
		return &ServiceabilityResult{Serviceable: false}, nil
	}
```

to:

```go
	// Polygons are non-overlapping, so the first containing polygon wins.
	// When IS_TEST is set, the polygon check is bypassed and the first active
	// darkstore is treated as the match.
	var matched *models.Darkstore
	if s.isTest {
		if len(darkstores) == 0 {
			return &ServiceabilityResult{Serviceable: false}, nil
		}
		matched = &darkstores[0]
		s.logger.Warn("IS_TEST is set; bypassing serviceability polygon check")
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

The rest of `CheckServiceability` (ETA, saved-address resolution, geocoding fallback) is unchanged.

- [ ] **Step 4: Verify the service package still compiles**

The package will fail to build because callers haven't been updated yet — that's expected. Just verify the file itself is syntactically valid by running:

Run: `cd /Users/shivangawasthi/bunzo/qcom && go vet ./internal/service/...`
Expected: errors about `cmd/server/main.go` and `tests/integration/upload_api_test.go` having too few arguments to `NewServiceabilityService` (these get fixed in Task 3). The `internal/service` package itself should report no errors.

If `go vet` reports errors *inside* `internal/service/serviceability_service.go`, fix them before continuing.

- [ ] **Step 5: Do NOT commit yet**

This change leaves the build broken; commit it together with Task 3 so no commit on `main` has a broken build.

---

### Task 3: Wire `cfg.IsTest` into every `NewServiceabilityService` caller

**Files:**
- Modify: `cmd/server/main.go:62`
- Modify: `tests/integration/upload_api_test.go:314`

- [ ] **Step 1: Update the production wiring in `main.go`**

In `cmd/server/main.go`, change line 62:

```go
	serviceabilityService := service.NewServiceabilityService(darkstoreRepo, addressService, geocoder, etaService, logger)
```

to:

```go
	serviceabilityService := service.NewServiceabilityService(darkstoreRepo, addressService, geocoder, etaService, logger, cfg.IsTest)
```

- [ ] **Step 2: Update the integration-test wiring in `upload_api_test.go`**

In `tests/integration/upload_api_test.go`, change line 314:

```go
	serviceabilityService := service.NewServiceabilityService(darkstoreRepo, addressService, testGeocoder, testETAService, logger)
```

to:

```go
	serviceabilityService := service.NewServiceabilityService(darkstoreRepo, addressService, testGeocoder, testETAService, logger, false)
```

The literal `false` here keeps the existing test behaviour (real polygon check) unchanged.

- [ ] **Step 3: Confirm there are no other callers**

Run: `cd /Users/shivangawasthi/bunzo/qcom && grep -rn "NewServiceabilityService(" --include="*.go" .`
Expected: exactly three matches — the definition in `internal/service/serviceability_service.go`, the call in `cmd/server/main.go`, and the call in `tests/integration/upload_api_test.go`. If a fourth caller exists, update it the same way (production code uses `cfg.IsTest`; tests pass a literal `false` unless they specifically exercise the bypass).

- [ ] **Step 4: Build the whole module**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go build ./...`
Expected: exits 0, no output.

- [ ] **Step 5: Vet the whole module**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go vet ./...`
Expected: exits 0, no output.

- [ ] **Step 6: Run unit tests**

Run: `cd /Users/shivangawasthi/bunzo/qcom && make test`
Expected: all tests pass. Integration tests under `tests/integration/` may be skipped or require Docker — that's fine; just ensure nothing newly fails.

- [ ] **Step 7: Smoke-test the bypass behavior manually**

This is a quick sanity check, not an automated test. From the project root:

```bash
cd /Users/shivangawasthi/bunzo/qcom
IS_TEST=true go run ./cmd/server &
SERVER_PID=$!
sleep 2
# Hit the serviceability endpoint with coordinates well outside any darkstore polygon.
# Replace the path/method/auth below with whatever the handler actually expects
# (see internal/handlers/serviceability_handlers.go for the exact route).
curl -sS -X POST http://localhost:8080/serviceability -H 'Content-Type: application/json' \
  -d '{"lat": 0.0, "lng": 0.0}'
kill $SERVER_PID
```

Expected: response has `"serviceable": true` and a `darkstore_id` (the first active darkstore). Then repeat without `IS_TEST=true` and confirm the same coordinates return `"serviceable": false`.

If the endpoint requires auth or different params, look at `internal/handlers/serviceability_handlers.go` and `tests/integration/serviceability_api_test.go` for the real request shape — the goal of this step is just to eyeball the bypass; you don't need to make the curl perfect.

- [ ] **Step 8: Commit**

```bash
cd /Users/shivangawasthi/bunzo/qcom
git add internal/service/serviceability_service.go cmd/server/main.go tests/integration/upload_api_test.go
git commit -m "serviceability: bypass polygon check when IS_TEST=true"
```

---

## Self-Review Notes

- **Spec coverage:** Config field (Task 1), service struct + constructor + gated polygon block (Task 2), all caller sites including the test wiring (Task 3). Behavior matrix from the spec is satisfied: `IS_TEST != "true"` keeps today's path; `IS_TEST="true"` with empty darkstores returns `Serviceable: false`; `IS_TEST="true"` with non-empty darkstores returns the first one.
- **Placeholder scan:** No TBDs. Every code change is shown in full. The curl in Task 3 Step 7 acknowledges the route/auth may differ and points the engineer at the source of truth rather than hand-waving.
- **Type consistency:** Field name `IsTest` (exported, on `Config`) and `isTest` (unexported, on `ServiceabilityService`) are used consistently throughout. Constructor parameter is `isTest bool` in both the definition and call sites; call sites pass `cfg.IsTest` (prod) or `false` (test).
