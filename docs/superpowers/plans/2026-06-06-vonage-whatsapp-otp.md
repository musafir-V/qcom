# Vonage WhatsApp OTP Integration — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the hardcoded `"112233"` OTP with real 6-digit OTPs delivered via Vonage Messages API over WhatsApp, using a JWT cached in DynamoDB.

**Architecture:** The app reads Vonage credentials from env vars (populated from SSM via `fetch-env.sh`). On each OTP send, a `VonageService` lazily fetches a cached Vonage JWT from DynamoDB (`PK: "VONAGE_JWT", SK: "TOKEN"`, 55-min TTL); if missing or expired it generates a fresh RS256 JWT and stores it. It then POSTs to `https://api.nexmo.com/v1/messages` with the `bunzo_login_otp` WhatsApp template. OTP is only stored in DynamoDB if Vonage confirms delivery; on failure the error is logged and returned.

**Tech Stack:** Go 1.24, `golang-jwt/jwt/v5` (already in go.mod), `github.com/google/uuid` (already in go.mod), AWS DynamoDB (existing table `QComTable`), Vonage Messages API.

---

## Files

| Action | Path | Responsibility |
|---|---|---|
| Modify | `internal/config/config.go` | Add `VonageConfig` struct, load from env, validate |
| Create | `internal/repository/vonage_jwt_repository.go` | DynamoDB cache: get/store Vonage JWT with TTL |
| Create | `internal/service/vonage_service.go` | JWT generation (RS256) + WhatsApp OTP send |
| Create | `internal/service/vonage_service_test.go` | Unit tests for JWT generation |
| Modify | `internal/service/otp_service.go` | Remove hardcoded OTP, inject VonageService, gate DynamoDB write on Vonage success |
| Modify | `cmd/server/main.go` | Wire VonageJWTRepository + VonageService into OTPService |
| Modify | `scripts/setup-ssm.sh` | Add Vonage SSM parameters |
| Modify | `.deploy.local.env.example` | Add Vonage env vars |

---

## Task 1: Add VonageConfig to config

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Step 1: Add `VonageConfig` struct and field to `Config`**

In `internal/config/config.go`, add the struct after `JavaConfig`:

```go
type VonageConfig struct {
	AppID          string
	PrivateKeyB64  string
	WhatsAppFrom   string
}
```

And add the field to `Config`:

```go
type Config struct {
	Server   ServerConfig
	DynamoDB DynamoDBConfig
	JWT      JWTConfig
	OTP      OTPConfig
	S3       S3Config
	Google   GoogleConfig
	Java     JavaConfig
	Vonage   VonageConfig
	IsTest   bool
}
```

- [ ] **Step 2: Load Vonage env vars in `Load()`**

Inside `Load()`, add after the `Java` block and before `IsTest`:

```go
Vonage: VonageConfig{
    AppID:         getEnv("VONAGE_APP_ID", ""),
    PrivateKeyB64: getEnv("VONAGE_PRIVATE_KEY", ""),
    WhatsAppFrom:  getEnv("VONAGE_WHATSAPP_FROM", ""),
},
```

- [ ] **Step 3: Validate Vonage fields**

After the `JWT_SECRET_KEY` validation in `Load()`, add:

```go
if cfg.Vonage.AppID == "" {
    return nil, fmt.Errorf("VONAGE_APP_ID environment variable is required")
}
if cfg.Vonage.PrivateKeyB64 == "" {
    return nil, fmt.Errorf("VONAGE_PRIVATE_KEY environment variable is required")
}
if cfg.Vonage.WhatsAppFrom == "" {
    return nil, fmt.Errorf("VONAGE_WHATSAPP_FROM environment variable is required")
}
```

- [ ] **Step 4: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./internal/config/...
```

Expected: no output, exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go
git commit -m "feat: add VonageConfig to app config"
```

---

## Task 2: Create Vonage JWT Repository

**Files:**
- Create: `internal/repository/vonage_jwt_repository.go`

This repository caches the Vonage-signed JWT in DynamoDB so the app doesn't re-sign on every OTP send. The item uses `PK: "VONAGE_JWT", SK: "TOKEN"` and a DynamoDB TTL of 55 minutes (5-minute buffer before the JWT's 1-hour expiry).

- [ ] **Step 1: Create the file**

```go
package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

const vonageJWTPK = "VONAGE_JWT"
const vonageJWTSK = "TOKEN"

type VonageJWTRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewVonageJWTRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *VonageJWTRepository {
	return &VonageJWTRepository{client: client, tableName: tableName, logger: logger}
}

// Get returns the cached JWT string, or ("", nil) if not found/expired.
func (r *VonageJWTRepository) Get(ctx context.Context) (string, error) {
	op := logging.Start(ctx, r.logger, "VonageJWTRepository.Get", nil)
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: vonageJWTPK},
			"SK": &types.AttributeValueMemberS{Value: vonageJWTSK},
		},
	})
	if err != nil {
		return "", op.Fail(fmt.Errorf("failed to get Vonage JWT: %w", err))
	}
	if result.Item == nil {
		return "", nil
	}

	tokenAttr, ok := result.Item["Token"].(*types.AttributeValueMemberS)
	if !ok || tokenAttr.Value == "" {
		return "", nil
	}
	return tokenAttr.Value, nil
}

// Store writes the JWT with a 55-minute TTL (DynamoDB auto-deletes after expiry).
func (r *VonageJWTRepository) Store(ctx context.Context, jwt string) error {
	op := logging.Start(ctx, r.logger, "VonageJWTRepository.Store", nil)
	defer op.End()

	ttl := time.Now().Add(55 * time.Minute).Unix()

	_, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item: map[string]types.AttributeValue{
			"PK":    &types.AttributeValueMemberS{Value: vonageJWTPK},
			"SK":    &types.AttributeValueMemberS{Value: vonageJWTSK},
			"Token": &types.AttributeValueMemberS{Value: jwt},
			"TTL":   &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", ttl)},
		},
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to store Vonage JWT: %w", err))
	}
	return nil
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./internal/repository/...
```

Expected: no output, exit 0.

- [ ] **Step 3: Commit**

```bash
git add internal/repository/vonage_jwt_repository.go
git commit -m "feat: add VonageJWT DynamoDB cache repository"
```

---

## Task 3: Create Vonage Service

**Files:**
- Create: `internal/service/vonage_service.go`
- Create: `internal/service/vonage_service_test.go`

This service owns two responsibilities:
1. Lazily get-or-refresh the cached Vonage JWT.
2. Send a WhatsApp OTP via the Vonage Messages API.

The JWT payload mirrors the structure from the confirmed working curl (empty string `acl` and `sub`, no `nbf`). The `to` phone number strips the leading `+` before sending to Vonage, since their API expects digits only.

- [ ] **Step 1: Write the failing test first**

Create `internal/service/vonage_service_test.go`:

```go
package service

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/pem"
	"crypto/x509"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func generateTestPrivateKey(t *testing.T) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate test RSA key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	pemBytes := pem.EncodeToMemory(block)
	b64 := base64.StdEncoding.EncodeToString(pemBytes)
	return b64, key
}

func TestGenerateVonageJWT_ClaimsAreCorrect(t *testing.T) {
	b64Key, pubKey := generateTestPrivateKey(t)
	appID := "test-app-id-123"

	svc := &VonageService{
		appID:         appID,
		privateKeyB64: b64Key,
	}

	before := time.Now().Unix()
	tokenStr, err := svc.generateJWT()
	after := time.Now().Unix()

	if err != nil {
		t.Fatalf("generateJWT returned error: %v", err)
	}
	if tokenStr == "" {
		t.Fatal("generateJWT returned empty token")
	}

	// Parse and verify with the public key
	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			t.Fatalf("unexpected signing method: %v", token.Header["alg"])
		}
		return &pubKey.PublicKey, nil
	})
	if err != nil {
		t.Fatalf("failed to parse generated JWT: %v", err)
	}
	if !token.Valid {
		t.Fatal("generated JWT is not valid")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("expected MapClaims")
	}

	if claims["application_id"] != appID {
		t.Errorf("application_id: got %v, want %v", claims["application_id"], appID)
	}

	iat := int64(claims["iat"].(float64))
	exp := int64(claims["exp"].(float64))

	if iat < before || iat > after {
		t.Errorf("iat %d not in range [%d, %d]", iat, before, after)
	}
	if exp-iat != 3600 {
		t.Errorf("exp-iat: got %d, want 3600", exp-iat)
	}

	if claims["sub"] != "" {
		t.Errorf("sub: got %v, want empty string", claims["sub"])
	}
	if claims["acl"] != "" {
		t.Errorf("acl: got %v, want empty string", claims["acl"])
	}

	jti, ok := claims["jti"].(string)
	if !ok || jti == "" {
		t.Error("jti must be a non-empty string")
	}
}

func TestGenerateVonageJWT_PartsCount(t *testing.T) {
	b64Key, _ := generateTestPrivateKey(t)
	svc := &VonageService{appID: "x", privateKeyB64: b64Key}

	tokenStr, err := svc.generateJWT()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 3 {
		t.Errorf("JWT must have 3 parts, got %d", len(parts))
	}
}
```

- [ ] **Step 2: Run the test to confirm it fails**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go test ./internal/service/... -run TestGenerateVonageJWT -v 2>&1 | tail -10
```

Expected: compile error — `VonageService` and `generateJWT` not defined yet.

- [ ] **Step 3: Create the implementation**

Create `internal/service/vonage_service.go`:

```go
package service

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

const vonageMessagesURL = "https://api.nexmo.com/v1/messages"
const vonageTemplateName = "bunzo_login_otp"

type VonageService struct {
	appID         string
	privateKeyB64 string
	whatsAppFrom  string
	jwtRepo       *repository.VonageJWTRepository
	httpClient    *http.Client
	logger        *logrus.Logger
}

func NewVonageService(cfg *config.VonageConfig, jwtRepo *repository.VonageJWTRepository, logger *logrus.Logger) *VonageService {
	return &VonageService{
		appID:         cfg.AppID,
		privateKeyB64: cfg.PrivateKeyB64,
		whatsAppFrom:  cfg.WhatsAppFrom,
		jwtRepo:       jwtRepo,
		httpClient:    &http.Client{Timeout: 10 * time.Second},
		logger:        logger,
	}
}

// getOrRefreshJWT returns a valid JWT from DynamoDB cache, generating a new one if needed.
func (s *VonageService) getOrRefreshJWT(ctx context.Context) (string, error) {
	cached, err := s.jwtRepo.Get(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to read JWT cache: %w", err)
	}
	if cached != "" {
		return cached, nil
	}

	token, err := s.generateJWT()
	if err != nil {
		return "", fmt.Errorf("failed to generate Vonage JWT: %w", err)
	}

	if err := s.jwtRepo.Store(ctx, token); err != nil {
		// Non-fatal: log and continue; we have a valid token for this request.
		s.logger.WithError(err).Warn("Failed to cache Vonage JWT in DynamoDB")
	}

	return token, nil
}

// generateJWT creates a signed RS256 JWT for the Vonage Messages API.
func (s *VonageService) generateJWT() (string, error) {
	privateKey, err := s.decodePrivateKey()
	if err != nil {
		return "", err
	}

	now := time.Now().Unix()

	type vonageClaims struct {
		ApplicationID string `json:"application_id"`
		Sub           string `json:"sub"`
		ACL           string `json:"acl"`
		jwt.RegisteredClaims
	}

	claims := vonageClaims{
		ApplicationID: s.appID,
		Sub:           "",
		ACL:           "",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Unix(now, 0)),
			ExpiresAt: jwt.NewNumericDate(time.Unix(now+3600, 0)),
			ID:        uuid.New().String(),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(privateKey)
}

func (s *VonageService) decodePrivateKey() (*rsa.PrivateKey, error) {
	pemBytes, err := base64.StdEncoding.DecodeString(s.privateKeyB64)
	if err != nil {
		return nil, fmt.Errorf("failed to base64-decode Vonage private key: %w", err)
	}

	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block from Vonage private key")
	}

	// Try PKCS8 first (Vonage typically provides PKCS8), fall back to PKCS1.
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("Vonage private key is not an RSA key")
		}
		return rsaKey, nil
	}

	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

// SendWhatsAppOTP delivers a 6-digit OTP via the bunzo_login_otp WhatsApp template.
func (s *VonageService) SendWhatsAppOTP(ctx context.Context, phoneNumber, otp string) error {
	op := logging.Start(ctx, s.logger, "VonageService.SendWhatsAppOTP", logrus.Fields{"phone": phoneNumber})
	defer op.End()

	jwtToken, err := s.getOrRefreshJWT(ctx)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to get Vonage JWT: %w", err))
	}

	// Vonage expects the phone number without leading '+'.
	to := strings.TrimPrefix(phoneNumber, "+")

	body := map[string]interface{}{
		"to":           to,
		"from":         s.whatsAppFrom,
		"channel":      "whatsapp",
		"message_type": "custom",
		"custom": map[string]interface{}{
			"type": "template",
			"template": map[string]interface{}{
				"name": vonageTemplateName,
				"language": map[string]string{
					"policy": "deterministic",
					"code":   "en_US",
				},
				"components": []map[string]interface{}{
					{
						"type": "body",
						"parameters": []map[string]string{
							{"type": "text", "text": otp},
						},
					},
					{
						"type":     "button",
						"sub_type": "url",
						"index":    "0",
						"parameters": []map[string]string{
							{"type": "text", "text": otp},
						},
					},
				},
			},
		},
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal Vonage request: %w", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, vonageMessagesURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return op.Fail(fmt.Errorf("failed to build Vonage request: %w", err))
	}
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return op.Fail(fmt.Errorf("Vonage API request failed: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errBody)
		return op.Fail(fmt.Errorf("Vonage API returned status %d: %v", resp.StatusCode, errBody))
	}

	op.With("outcome", "sent")
	return nil
}
```

- [ ] **Step 4: Run the tests — they must pass**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go test ./internal/service/... -run TestGenerateVonageJWT -v 2>&1 | tail -15
```

Expected:
```
--- PASS: TestGenerateVonageJWT_ClaimsAreCorrect (...)
--- PASS: TestGenerateVonageJWT_PartsCount (...)
PASS
```

- [ ] **Step 5: Commit**

```bash
git add internal/service/vonage_service.go internal/service/vonage_service_test.go
git commit -m "feat: add VonageService with RS256 JWT generation and WhatsApp OTP send"
```

---

## Task 4: Update OTP Service

**Files:**
- Modify: `internal/service/otp_service.go`

Remove the hardcoded OTP, inject `VonageService`, and only persist the OTP in DynamoDB after Vonage confirms the message was sent. Also remove `StoreTestOTP` (no longer needed).

- [ ] **Step 1: Rewrite `otp_service.go`**

Replace the entire file with:

```go
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

type OTPService struct {
	otpRepo       *repository.OTPRepository
	vonageService *VonageService
	cfg           *config.OTPConfig
	logger        *logrus.Logger
}

func NewOTPService(otpRepo *repository.OTPRepository, vonageService *VonageService, cfg *config.OTPConfig, logger *logrus.Logger) *OTPService {
	return &OTPService{
		otpRepo:       otpRepo,
		vonageService: vonageService,
		cfg:           cfg,
		logger:        logger,
	}
}

func (s *OTPService) GenerateOTP(ctx context.Context, phoneNumber string) (string, error) {
	op := logging.Start(ctx, s.logger, "GenerateOTP", logrus.Fields{"phone": phoneNumber})
	defer op.End()

	otp, err := generateRandomOTP(s.cfg.Length)
	if err != nil {
		return "", op.Fail(fmt.Errorf("failed to generate OTP: %w", err))
	}

	if err := s.vonageService.SendWhatsAppOTP(ctx, phoneNumber, otp); err != nil {
		op.Logger().WithError(err).Error("Failed to send OTP via Vonage WhatsApp")
		return "", op.Fail(fmt.Errorf("failed to send OTP: %w", err))
	}

	hashedOTP, err := bcrypt.GenerateFromPassword([]byte(otp), bcrypt.DefaultCost)
	if err != nil {
		return "", op.Fail(fmt.Errorf("failed to hash OTP: %w", err))
	}

	otpData := models.OTPData{
		OTPHash:   string(hashedOTP),
		Phone:     phoneNumber,
		Attempts:  0,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(s.cfg.Expiry),
	}

	if err := s.otpRepo.Store(ctx, phoneNumber, otpData); err != nil {
		return "", op.Fail(err)
	}

	op.With("outcome", "sent")
	return otp, nil
}

func (s *OTPService) VerifyOTP(ctx context.Context, phoneNumber, otp string) (bool, error) {
	op := logging.Start(ctx, s.logger, "VerifyOTP", logrus.Fields{"phone": phoneNumber})
	defer op.End()

	otpData, err := s.otpRepo.Get(ctx, phoneNumber)
	if err != nil {
		return false, op.Fail(err)
	}

	if time.Now().After(otpData.ExpiresAt) {
		s.otpRepo.Delete(ctx, phoneNumber)
		return false, op.Outcome("expired", fmt.Errorf("OTP expired"))
	}

	if otpData.Attempts >= s.cfg.MaxAttempts {
		s.otpRepo.Delete(ctx, phoneNumber)
		return false, op.Outcome("max_attempts", fmt.Errorf("maximum attempts exceeded"))
	}

	err = bcrypt.CompareHashAndPassword([]byte(otpData.OTPHash), []byte(otp))
	if err != nil {
		otpData.Attempts++
		s.otpRepo.Store(ctx, phoneNumber, *otpData)
		return false, op.Outcome("invalid", fmt.Errorf("invalid OTP"))
	}

	s.otpRepo.Delete(ctx, phoneNumber)
	op.With("outcome", "verified")
	return true, nil
}
```

Note: `generateRandomOTP` is now a package-level function (not a method) since it has no service state. Move it there by adding this at the bottom of the file:

```go
func generateRandomOTP(length int) (string, error) {
	const digits = "0123456789"
	otp := make([]byte, length)
	for i := range otp {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		if err != nil {
			return "", err
		}
		otp[i] = digits[n.Int64()]
	}
	return string(otp), nil
}
```

Add the missing imports at the top of the file:

```go
import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./internal/service/...
```

Expected: no output, exit 0.

- [ ] **Step 3: Commit**

```bash
git add internal/service/otp_service.go
git commit -m "feat: wire VonageService into OTPService, remove hardcoded OTP"
```

---

## Task 5: Wire everything up in main.go

**Files:**
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Add VonageJWTRepository and VonageService, update OTPService construction**

In `main()`, after the `otpRepo` line, add:

```go
vonageJWTRepo := repository.NewVonageJWTRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
```

After the `jwtService` block, replace:

```go
otpService := service.NewOTPService(otpRepo, &cfg.OTP, logger)
```

with:

```go
vonageService := service.NewVonageService(&cfg.Vonage, vonageJWTRepo, logger)
otpService := service.NewOTPService(otpRepo, vonageService, &cfg.OTP, logger)
```

- [ ] **Step 2: Build the whole binary**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./cmd/server/...
```

Expected: no output, exit 0.

- [ ] **Step 3: Run all unit tests**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go test ./... 2>&1 | tail -20
```

Expected: all tests pass (PASS lines, no FAIL). Tests that require DynamoDB/network will be skipped or use local config.

- [ ] **Step 4: Remove the stale comment in auth_handlers.go**

In `internal/handlers/auth_handlers.go`, remove these two comment lines (they're now inaccurate):

```go
// OTP is logged in the service (for development)
// In production, send via WhatsApp here
```

- [ ] **Step 5: Commit**

```bash
git add cmd/server/main.go internal/handlers/auth_handlers.go
git commit -m "feat: wire Vonage repositories and services in main"
```

---

## Task 6: Update deployment scripts and env example

**Files:**
- Modify: `scripts/setup-ssm.sh`
- Modify: `.deploy.local.env.example`

- [ ] **Step 1: Add Vonage prompts to `setup-ssm.sh`**

After the `GOOGLE_MAPS_API_KEY` block and before the `PORT` block, add:

```bash
read -r -p "VONAGE_APP_ID: " VONAGE_APP_ID
read -r -p "VONAGE_PRIVATE_KEY (base64-encoded PEM, no newlines): " VONAGE_PRIVATE_KEY
read -r -p "VONAGE_WHATSAPP_FROM (digits only, no +, e.g. 15559615672): " VONAGE_WHATSAPP_FROM
```

And in the `put_param` section, add:

```bash
[ -n "${VONAGE_APP_ID}" ]      && put_param "/qcom/prod/VONAGE_APP_ID"      "${VONAGE_APP_ID}"
[ -n "${VONAGE_PRIVATE_KEY}" ] && put_param "/qcom/prod/VONAGE_PRIVATE_KEY" "${VONAGE_PRIVATE_KEY}"
[ -n "${VONAGE_WHATSAPP_FROM}" ] && put_param "/qcom/prod/VONAGE_WHATSAPP_FROM" "${VONAGE_WHATSAPP_FROM}"
```

- [ ] **Step 2: Add Vonage vars to `.deploy.local.env.example`**

Append to the file:

```bash
# Vonage WhatsApp OTP
# base64-encode your PEM: base64 -i vonage.key | tr -d '\n'
VONAGE_APP_ID=
VONAGE_PRIVATE_KEY=
VONAGE_WHATSAPP_FROM=
```

- [ ] **Step 3: Commit**

```bash
git add scripts/setup-ssm.sh .deploy.local.env.example
git commit -m "feat: add Vonage credentials to SSM setup script and env example"
```

---

## Self-Review

### Spec coverage

| Requirement | Task |
|---|---|
| Vonage app ID + private key in env (not code) | Task 1, 6 |
| Private key base64-encoded | Task 3 (`decodePrivateKey`) + Task 6 (docs) |
| JWT generated from private key + app ID (RS256) | Task 3 (`generateJWT`) |
| JWT cached in DynamoDB with 55-min TTL | Task 2 + Task 3 (`getOrRefreshJWT`) |
| JWT reused until TTL expires | Task 2 (`Get` returns cached token) |
| Hardcoded `"112233"` removed | Task 4 |
| Random 6-digit OTP | Task 4 (`generateRandomOTP`) |
| OTP sent via WhatsApp template `bunzo_login_otp` | Task 3 (`SendWhatsAppOTP`) |
| OTP only stored in DynamoDB after Vonage success | Task 4 (Vonage send before `otpRepo.Store`) |
| Vonage failure logged + returned as error | Task 3 + Task 4 |
| `StoreTestOTP` removed | Task 4 |
| `VONAGE_WHATSAPP_FROM` as env var | Task 1, 6 |
| SSM + EC2 deployment | Task 6 |
| `IS_TEST` unchanged | Not touched |

### Placeholder scan

No TBDs, no TODOs, no "similar to" references. All code blocks are complete.

### Type consistency

- `VonageService` struct defined in Task 3, referenced in Task 4 (`*VonageService`) and Task 5 — consistent.
- `NewOTPService` signature: Task 4 defines `(otpRepo, vonageService, cfg, logger)`, Task 5 calls it the same way — consistent.
- `NewVonageJWTRepository` defined in Task 2, called in Task 5 — consistent.
- `VonageConfig` defined in Task 1, used in Task 3 `NewVonageService(&cfg.Vonage, ...)` — consistent.
