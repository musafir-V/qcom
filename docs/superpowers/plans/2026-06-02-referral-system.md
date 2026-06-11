# Referral System Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the driver referral system — each DE gets a static 6-digit referral code, a new DE can enter it at registration to link themselves to the referrer, and when the referred DE completes `referral_trips_threshold` trips within `referral_window_days`, both DEs earn `referral_bonus_zmw` added to their outstanding balance.

**Architecture:** Referral code is generated once at DE registration and stored on the DE model. A `ReferralCodeIndex` GSI on the DE table enables O(1) lookup of referrer by code. Referral relationship is stored as a `REFERRAL!{referredDeId}` DynamoDB item. Bonus is triggered inline at trip completion by `ReferralService.CheckAndTriggerBonus` — called from `TripService` when a trip is marked completed. Payout config variables (`referral_trips_threshold`, `referral_window_days`, `referral_bonus_zmw`) are read from the `CONFIG / PAYOUT_V1` DynamoDB item.

**Tech Stack:** Go 1.24, AWS DynamoDB (aws-sdk-go-v2), gorilla/mux, logrus, uuid

**Prerequisites:**
- `internal/models/delivery_executive.go` — DE model must have `ReferralCode string` field (added in this plan)
- `internal/repository/de_repository.go` — must expose `GetByReferralCode` (added in this plan)
- `internal/repository/payout_config_repository.go` and `internal/models/payout_config.go` must exist with `ReferralTripsThreshold`, `ReferralWindowDays`, `ReferralBonusZMW` fields — **add stub versions in Task 1 if Plan B hasn't been executed yet**
- `TripService.CompleteTrip` must call `ReferralService.CheckAndTriggerBonus` — wired in Task 8

---

## File Map

### New Files
- `internal/models/referral.go` — Referral struct + status constants
- `internal/models/payout_config.go` — PayoutConfig stub (referral fields only; Plan B expands this)
- `internal/repository/referral_repository.go` — Referral CRUD + existence check
- `internal/repository/payout_config_repository.go` — PayoutConfig GetItem (stub for referral fields)
- `internal/service/referral_service.go` — Code generation, registration linking, bonus trigger
- `internal/service/referral_service_test.go` — Unit tests for referral service
- `internal/handlers/referral_handlers.go` — GET /de/referral

### Modified Files
- `internal/models/delivery_executive.go` — Add `ReferralCode string`
- `internal/repository/de_repository.go` — Add `GetByReferralCode`, `SetReferralCode`
- `internal/service/de_service.go` — Update `Register` to generate + store referral code; add referral linking
- `internal/handlers/de_handlers.go` — Update `Register` to accept optional `referral_code` field
- `cmd/server/main.go` — Wire referral repo, service, handler; add route

---

## Task 1: Referral and PayoutConfig Models

**Files:**
- Create: `internal/models/referral.go`
- Create: `internal/models/payout_config.go`

- [ ] **Step 1: Write the referral model**

```go
// internal/models/referral.go
package models

type ReferralStatus string

const (
	ReferralStatusActive    ReferralStatus = "active"
	ReferralStatusCompleted ReferralStatus = "completed"
	ReferralStatusExpired   ReferralStatus = "expired"
)

type Referral struct {
	ReferrerDEID      string         `json:"referrer_de_id" dynamodbav:"referrer_de_id"`
	ReferredDEID      string         `json:"referred_de_id" dynamodbav:"referred_de_id"`
	Status            ReferralStatus `json:"status" dynamodbav:"status"`
	CreatedAt         string         `json:"created_at" dynamodbav:"created_at"`
	WindowExpiresAt   string         `json:"window_expires_at" dynamodbav:"window_expires_at"`
	PayoutTriggeredAt string         `json:"payout_triggered_at,omitempty" dynamodbav:"payout_triggered_at,omitempty"`
}

func (r *Referral) GetPK() string { return "REFERRAL!" + r.ReferredDEID }
func (r *Referral) GetSK() string { return "METADATA" }
```

- [ ] **Step 2: Write the payout config model (referral fields only — Plan B expands)**

```go
// internal/models/payout_config.go
package models

type PayoutConfig struct {
	// Referral
	ReferralTripsThreshold int     `json:"referral_trips_threshold" dynamodbav:"referral_trips_threshold"`
	ReferralWindowDays     int     `json:"referral_window_days" dynamodbav:"referral_window_days"`
	ReferralBonusZMW       float64 `json:"referral_bonus_zmw" dynamodbav:"referral_bonus_zmw"`

	// Distance-based pay (populated by Plan B)
	RatePerKmZMW    float64 `json:"rate_per_km_zmw" dynamodbav:"rate_per_km_zmw"`
	Tier1Threshold  int     `json:"tier1_threshold" dynamodbav:"tier1_threshold"`
	Tier1BonusZMW   float64 `json:"tier1_bonus_zmw" dynamodbav:"tier1_bonus_zmw"`
	Tier2Threshold  int     `json:"tier2_threshold" dynamodbav:"tier2_threshold"`
	Tier2BonusZMW   float64 `json:"tier2_bonus_zmw" dynamodbav:"tier2_bonus_zmw"`

	// Weekly bonus (populated by Plan B)
	MinDeliveriesPerDay int     `json:"min_deliveries_per_day" dynamodbav:"min_deliveries_per_day"`
	WeeklyW1Days        int     `json:"weekly_w1_days" dynamodbav:"weekly_w1_days"`
	WeeklyW1BonusZMW    float64 `json:"weekly_w1_bonus_zmw" dynamodbav:"weekly_w1_bonus_zmw"`
	WeeklyW2Days        int     `json:"weekly_w2_days" dynamodbav:"weekly_w2_days"`
	WeeklyW2BonusZMW    float64 `json:"weekly_w2_bonus_zmw" dynamodbav:"weekly_w2_bonus_zmw"`
	WeeklyW3Days        int     `json:"weekly_w3_days" dynamodbav:"weekly_w3_days"`
	WeeklyW3BonusZMW    float64 `json:"weekly_w3_bonus_zmw" dynamodbav:"weekly_w3_bonus_zmw"`
}

func (p *PayoutConfig) GetPK() string { return "CONFIG" }
func (p *PayoutConfig) GetSK() string { return "PAYOUT_V1" }
```

- [ ] **Step 3: Commit**

```bash
git add internal/models/referral.go internal/models/payout_config.go
git commit -m "feat: add Referral and PayoutConfig models"
```

---

## Task 2: Update DE Model — Add ReferralCode

**Files:**
- Modify: `internal/models/delivery_executive.go`

- [ ] **Step 1: Add `ReferralCode` field to DeliveryExecutive**

In `internal/models/delivery_executive.go`, add after `CurrentOrderID`:

```go
ReferralCode string `json:"referral_code,omitempty" dynamodbav:"referral_code,omitempty"`
```

The full struct now ends with:
```go
type DeliveryExecutive struct {
	DEID           string   `json:"de_id" dynamodbav:"de_id"`
	PhoneNumber    string   `json:"phone_number" dynamodbav:"phone_number"`
	Name           string   `json:"name" dynamodbav:"name"`
	ProfileURL     string   `json:"profile_url" dynamodbav:"profile_url"`
	NRCURL         string   `json:"nrc_url" dynamodbav:"nrc_url"`
	Status         DEStatus `json:"status" dynamodbav:"status"`
	DutyIndexKey   string   `json:"duty_index_key,omitempty" dynamodbav:"duty_index_key,omitempty"`
	CurrentStoreID string   `json:"current_store_id,omitempty" dynamodbav:"current_store_id,omitempty"`
	CurrentOrderID string   `json:"current_order_id,omitempty" dynamodbav:"current_order_id,omitempty"`
	ReferralCode        string   `json:"referral_code,omitempty" dynamodbav:"referral_code,omitempty"`
	TotalTripsCompleted int      `json:"total_trips_completed" dynamodbav:"total_trips_completed"`
	CreatedAt           string   `json:"created_at" dynamodbav:"created_at"`
	UpdatedAt           string   `json:"updated_at" dynamodbav:"updated_at"`
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./internal/models/...
```

Expected: no output (success)

- [ ] **Step 3: Commit**

```bash
git add internal/models/delivery_executive.go
git commit -m "feat: add ReferralCode field to DeliveryExecutive model"
```

---

## Task 3: Payout Config Repository

**Files:**
- Create: `internal/repository/payout_config_repository.go`

- [ ] **Step 1: Write the repository**

```go
// internal/repository/payout_config_repository.go
package repository

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type PayoutConfigRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewPayoutConfigRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *PayoutConfigRepository {
	return &PayoutConfigRepository{client: client, tableName: tableName, logger: logger}
}

func (r *PayoutConfigRepository) Get(ctx context.Context) (*models.PayoutConfig, error) {
	op := logging.Start(ctx, r.logger, "PayoutConfigRepository.Get", nil)
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "CONFIG"},
			"SK": &types.AttributeValueMemberS{Value: "PAYOUT_V1"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get payout config: %w", err))
	}
	if result.Item == nil {
		return nil, op.Outcome("not_found", fmt.Errorf("payout config not found"))
	}

	var cfg models.PayoutConfig
	if err := attributevalue.UnmarshalMap(result.Item, &cfg); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal payout config: %w", err))
	}
	return &cfg, nil
}

// UpdateField updates a single attribute in the payout config item.
// fieldName must match the DynamoDB attribute name (snake_case).
// value must be a valid DynamoDB AttributeValue.
func (r *PayoutConfigRepository) UpdateField(ctx context.Context, fieldName string, value types.AttributeValue) error {
	op := logging.Start(ctx, r.logger, "PayoutConfigRepository.UpdateField", logrus.Fields{"field": fieldName})
	defer op.End()

	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "CONFIG"},
			"SK": &types.AttributeValueMemberS{Value: "PAYOUT_V1"},
		},
		UpdateExpression: aws.String("SET #field = :value"),
		ExpressionAttributeNames: map[string]string{
			"#field": fieldName,
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":value": value,
		},
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to update payout config field %s: %w", fieldName, err))
	}
	return nil
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./internal/repository/...
```

Expected: no output

- [ ] **Step 3: Commit**

```bash
git add internal/repository/payout_config_repository.go
git commit -m "feat: add PayoutConfigRepository with Get and UpdateField"
```

---

## Task 4: Referral Repository + DE Repository Updates

**Files:**
- Create: `internal/repository/referral_repository.go`
- Modify: `internal/repository/de_repository.go`

- [ ] **Step 1: Write the referral repository**

```go
// internal/repository/referral_repository.go
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type ReferralRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewReferralRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *ReferralRepository {
	return &ReferralRepository{client: client, tableName: tableName, logger: logger}
}

func (r *ReferralRepository) Create(ctx context.Context, ref *models.Referral) error {
	op := logging.Start(ctx, r.logger, "ReferralRepository.Create", logrus.Fields{
		"referrer": ref.ReferrerDEID, "referred": ref.ReferredDEID,
	})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)
	ref.CreatedAt = now
	ref.Status = models.ReferralStatusActive

	item, err := attributevalue.MarshalMap(ref)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal referral: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: ref.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: ref.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(r.tableName),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return op.Outcome("already_exists", fmt.Errorf("referred DE already has a referral"))
		}
		return op.Fail(fmt.Errorf("failed to create referral: %w", err))
	}
	return nil
}

func (r *ReferralRepository) GetByReferredDEID(ctx context.Context, referredDEID string) (*models.Referral, error) {
	op := logging.Start(ctx, r.logger, "ReferralRepository.GetByReferredDEID", logrus.Fields{"referred_de_id": referredDEID})
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "REFERRAL!" + referredDEID},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get referral: %w", err))
	}
	if result.Item == nil {
		return nil, nil
	}

	var ref models.Referral
	if err := attributevalue.UnmarshalMap(result.Item, &ref); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal referral: %w", err))
	}
	return &ref, nil
}

func (r *ReferralRepository) MarkCompleted(ctx context.Context, referredDEID, triggeredAt string) error {
	op := logging.Start(ctx, r.logger, "ReferralRepository.MarkCompleted", logrus.Fields{"referred_de_id": referredDEID})
	defer op.End()

	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "REFERRAL!" + referredDEID},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression: aws.String("SET #status = :completed, payout_triggered_at = :triggered"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":completed": &types.AttributeValueMemberS{Value: string(models.ReferralStatusCompleted)},
			":triggered": &types.AttributeValueMemberS{Value: triggeredAt},
			":active":    &types.AttributeValueMemberS{Value: string(models.ReferralStatusActive)},
		},
		ConditionExpression: aws.String("#status = :active"),
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return op.Outcome("already_completed", fmt.Errorf("referral already completed or expired"))
		}
		return op.Fail(fmt.Errorf("failed to mark referral completed: %w", err))
	}
	return nil
}

// ListByReferrerDEID scans for referrals where referrer_de_id matches.
// Uses a filter scan — add a GSI on referrer_de_id if referrer list becomes large.
func (r *ReferralRepository) ListByReferrerDEID(ctx context.Context, referrerDEID string) ([]*models.Referral, error) {
	op := logging.Start(ctx, r.logger, "ReferralRepository.ListByReferrerDEID", logrus.Fields{"referrer_de_id": referrerDEID})
	defer op.End()

	result, err := r.client.Scan(ctx, &dynamodb.ScanInput{
		TableName:        aws.String(r.tableName),
		FilterExpression: aws.String("referrer_de_id = :rid AND begins_with(PK, :prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":rid":    &types.AttributeValueMemberS{Value: referrerDEID},
			":prefix": &types.AttributeValueMemberS{Value: "REFERRAL!"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to list referrals by referrer: %w", err))
	}

	var refs []*models.Referral
	for _, item := range result.Items {
		var ref models.Referral
		if err := attributevalue.UnmarshalMap(item, &ref); err != nil {
			continue
		}
		refs = append(refs, &ref)
	}
	op.With("count", len(refs))
	return refs, nil
}
```

- [ ] **Step 2: Add `GetByReferralCode` and `SetReferralCode` to DE repository**

Add these two methods at the bottom of `internal/repository/de_repository.go`:

```go
// GetByReferralCode looks up a DE using the ReferralCodeIndex GSI.
// The GSI must be configured in DynamoDB: index name "ReferralCodeIndex",
// partition key "referral_code", projecting all attributes.
func (r *DERepository) GetByReferralCode(ctx context.Context, code string) (*models.DeliveryExecutive, error) {
	op := logging.Start(ctx, r.logger, "GetByReferralCode", logrus.Fields{"code": code})
	defer op.End()

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		IndexName:              aws.String("ReferralCodeIndex"),
		KeyConditionExpression: aws.String("referral_code = :code"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":code": &types.AttributeValueMemberS{Value: code},
		},
		Limit: aws.Int32(1),
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to query by referral code: %w", err))
	}
	if len(result.Items) == 0 {
		op.With("found", false)
		return nil, nil
	}

	var de models.DeliveryExecutive
	if err := attributevalue.UnmarshalMap(result.Items[0], &de); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal DE: %w", err))
	}
	op.With("found", true)
	return &de, nil
}

```

- [ ] **Step 3: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./internal/repository/...
```

Expected: no output

- [ ] **Step 4: Commit**

```bash
git add internal/repository/referral_repository.go internal/repository/de_repository.go
git commit -m "feat: add ReferralRepository and DE referral code lookup methods"
```

---

## Task 5: Referral Service

**Files:**
- Create: `internal/service/referral_service.go`
- Create: `internal/service/referral_service_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/service/referral_service_test.go
package service

import (
	"testing"
	"time"
)

func TestGenerateReferralCode_IsNumeric(t *testing.T) {
	for i := 0; i < 100; i++ {
		code := generateReferralCode()
		if len(code) != 6 {
			t.Fatalf("expected 6-digit code, got %q (len %d)", code, len(code))
		}
		for _, c := range code {
			if c < '0' || c > '9' {
				t.Fatalf("expected numeric code, got %q", code)
			}
		}
	}
}

func TestIsWithinReferralWindow_Inside(t *testing.T) {
	createdAt := time.Now().UTC().Add(-5 * 24 * time.Hour).Format(time.RFC3339)
	if !isWithinReferralWindow(createdAt, 10) {
		t.Fatal("expected to be within 10-day window after 5 days")
	}
}

func TestIsWithinReferralWindow_Outside(t *testing.T) {
	createdAt := time.Now().UTC().Add(-11 * 24 * time.Hour).Format(time.RFC3339)
	if isWithinReferralWindow(createdAt, 10) {
		t.Fatal("expected to be outside 10-day window after 11 days")
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go test ./internal/service/... -run "TestGenerateReferralCode|TestIsWithinReferralWindow" -v
```

Expected: `FAIL` — `generateReferralCode` and `isWithinReferralWindow` undefined

- [ ] **Step 3: Write the referral service**

```go
// internal/service/referral_service.go
package service

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

type ReferralService struct {
	referralRepo      *repository.ReferralRepository
	deRepo            *repository.DERepository
	payoutConfigRepo  *repository.PayoutConfigRepository
	logger            *logrus.Logger
}

func NewReferralService(
	referralRepo *repository.ReferralRepository,
	deRepo *repository.DERepository,
	payoutConfigRepo *repository.PayoutConfigRepository,
	logger *logrus.Logger,
) *ReferralService {
	return &ReferralService{
		referralRepo:     referralRepo,
		deRepo:           deRepo,
		payoutConfigRepo: payoutConfigRepo,
		logger:           logger,
	}
}

// GenerateUniqueCode generates a 6-digit numeric code that is not already
// used by another DE. Retries up to 10 times before returning an error.
func (s *ReferralService) GenerateUniqueCode(ctx context.Context) (string, error) {
	op := logging.Start(ctx, s.logger, "ReferralService.GenerateUniqueCode", nil)
	defer op.End()

	for attempt := 0; attempt < 10; attempt++ {
		code := generateReferralCode()
		existing, err := s.deRepo.GetByReferralCode(ctx, code)
		if err != nil {
			return "", op.Fail(fmt.Errorf("failed to check referral code uniqueness: %w", err))
		}
		if existing == nil {
			return code, nil
		}
	}
	return "", op.Fail(fmt.Errorf("failed to generate unique referral code after 10 attempts"))
}

// LinkReferral creates the referral relationship when a new DE registers with a referral code.
// referredDEID is the UUID of the newly registered DE.
// referralCode is the code the new DE provided during registration.
func (s *ReferralService) LinkReferral(ctx context.Context, referredDEID, referralCode string, windowDays int) error {
	op := logging.Start(ctx, s.logger, "ReferralService.LinkReferral", logrus.Fields{
		"referred_de_id": referredDEID,
		"referral_code":  referralCode,
	})
	defer op.End()

	referrer, err := s.deRepo.GetByReferralCode(ctx, referralCode)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to look up referrer: %w", err))
	}
	if referrer == nil {
		return op.Outcome("invalid_code", fmt.Errorf("referral code %q not found", referralCode))
	}

	now := time.Now().UTC()
	windowExpires := now.Add(time.Duration(windowDays) * 24 * time.Hour).Format(time.RFC3339)

	ref := &models.Referral{
		ReferrerDEID:    referrer.DEID,
		ReferredDEID:    referredDEID,
		Status:          models.ReferralStatusActive,
		WindowExpiresAt: windowExpires,
	}
	if err := s.referralRepo.Create(ctx, ref); err != nil {
		return op.Fail(fmt.Errorf("failed to create referral: %w", err))
	}

	op.With("referrer_de_id", referrer.DEID)
	return nil
}

// CheckAndTriggerBonus is called after every trip completion for the DE.
// totalTripsCompleted is the DE's all-time completed trip count.
// If the DE has an active referral and has hit the threshold within the window,
// it marks the referral completed and returns the bonus amount to credit to both DEs.
// Returns (bonusZMW, referrerDEID, error). bonusZMW=0 means no bonus triggered.
func (s *ReferralService) CheckAndTriggerBonus(ctx context.Context, referredDEID string, totalTripsCompleted int) (float64, string, error) {
	op := logging.Start(ctx, s.logger, "ReferralService.CheckAndTriggerBonus", logrus.Fields{
		"referred_de_id":        referredDEID,
		"total_trips_completed": totalTripsCompleted,
	})
	defer op.End()

	cfg, err := s.payoutConfigRepo.Get(ctx)
	if err != nil {
		return 0, "", op.Fail(fmt.Errorf("failed to get payout config: %w", err))
	}

	if totalTripsCompleted < cfg.ReferralTripsThreshold {
		return 0, "", nil
	}

	ref, err := s.referralRepo.GetByReferredDEID(ctx, referredDEID)
	if err != nil {
		return 0, "", op.Fail(fmt.Errorf("failed to get referral: %w", err))
	}
	if ref == nil || ref.Status != models.ReferralStatusActive {
		return 0, "", nil
	}

	if !isWithinReferralWindow(ref.CreatedAt, cfg.ReferralWindowDays) {
		return 0, "", nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if err := s.referralRepo.MarkCompleted(ctx, referredDEID, now); err != nil {
		// "already_completed" outcome means a concurrent call won the race — not an error
		if !strings.Contains(err.Error(), "already completed") {
			return 0, "", op.Fail(err)
		}
		return 0, "", nil
	}

	op.With("bonus_zmw", cfg.ReferralBonusZMW).With("referrer_de_id", ref.ReferrerDEID)
	return cfg.ReferralBonusZMW, ref.ReferrerDEID, nil
}

// GetReferralScreen returns the DE's referral code and list of referrals they initiated.
func (s *ReferralService) GetReferralScreen(ctx context.Context, deID, dePhone string) (string, []*models.Referral, error) {
	op := logging.Start(ctx, s.logger, "ReferralService.GetReferralScreen", logrus.Fields{"de_id": deID})
	defer op.End()

	de, err := s.deRepo.GetByPhone(ctx, dePhone)
	if err != nil || de == nil {
		return "", nil, op.Fail(fmt.Errorf("failed to fetch DE: %w", err))
	}

	refs, err := s.referralRepo.ListByReferrerDEID(ctx, deID)
	if err != nil {
		return "", nil, op.Fail(err)
	}

	return de.ReferralCode, refs, nil
}

// generateReferralCode returns a random 6-digit numeric string (000000–999999).
func generateReferralCode() string {
	return fmt.Sprintf("%06d", rand.Intn(1000000))
}

// isWithinReferralWindow returns true if createdAt is within windowDays days of now.
func isWithinReferralWindow(createdAt string, windowDays int) bool {
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return false
	}
	return time.Since(t) <= time.Duration(windowDays)*24*time.Hour
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go test ./internal/service/... -run "TestGenerateReferralCode|TestIsWithinReferralWindow" -v
```

Expected:
```
--- PASS: TestGenerateReferralCode_IsNumeric
--- PASS: TestIsWithinReferralWindow_Inside
--- PASS: TestIsWithinReferralWindow_Outside
```

- [ ] **Step 5: Commit**

```bash
git add internal/service/referral_service.go internal/service/referral_service_test.go
git commit -m "feat: add ReferralService with code generation, linking, and bonus trigger"
```

---

## Task 6: Update DE Service — Register with Referral Code

**Files:**
- Modify: `internal/service/de_service.go`

- [ ] **Step 1: Add `ReferralService` dependency and update `Register`**

Update `DEService` struct and `NewDEService`:

```go
type DEService struct {
	deRepo          *repository.DERepository
	qrService       *QRService
	referralService *ReferralService
	logger          *logrus.Logger
}

func NewDEService(
	deRepo *repository.DERepository,
	qrService *QRService,
	referralService *ReferralService,
	logger *logrus.Logger,
) *DEService {
	return &DEService{
		deRepo:          deRepo,
		qrService:       qrService,
		referralService: referralService,
		logger:          logger,
	}
}
```

Update `RegisterDERequest` to include referral code:

```go
type RegisterDERequest struct {
	PhoneNumber  string
	Name         string
	ProfileURL   string
	NRCURL       string
	ReferralCode string // optional
}
```

Replace the `Register` method body with:

```go
func (s *DEService) Register(ctx context.Context, req RegisterDERequest) (*models.DeliveryExecutive, error) {
	op := logging.Start(ctx, s.logger, "Register", logrus.Fields{"phone": req.PhoneNumber})
	defer op.End()

	// Generate a unique referral code for this new DE
	referralCode, err := s.referralService.GenerateUniqueCode(ctx)
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to generate referral code: %w", err))
	}

	de := &models.DeliveryExecutive{
		DEID:         uuid.New().String(),
		PhoneNumber:  req.PhoneNumber,
		Name:         req.Name,
		ProfileURL:   req.ProfileURL,
		NRCURL:       req.NRCURL,
		Status:       models.DEStatusOffline,
		ReferralCode: referralCode,
	}

	if err := s.deRepo.Create(ctx, de); err != nil {
		return nil, op.Fail(err)
	}

	// Link referral if a code was provided — non-fatal if invalid
	if req.ReferralCode != "" {
		cfg, cfgErr := s.referralService.payoutConfigRepo.Get(ctx)
		windowDays := 30 // safe default if config unavailable
		if cfgErr == nil {
			windowDays = cfg.ReferralWindowDays
		}
		if linkErr := s.referralService.LinkReferral(ctx, de.DEID, req.ReferralCode, windowDays); linkErr != nil {
			s.logger.WithError(linkErr).Warn("referral linking failed during registration — continuing")
		}
	}

	return de, nil
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./internal/service/...
```

Expected: no output

- [ ] **Step 3: Commit**

```bash
git add internal/service/de_service.go
git commit -m "feat: generate referral code at DE registration, link referral if code provided"
```

---

## Task 7: Update DE Register Handler

**Files:**
- Modify: `internal/handlers/de_handlers.go`

- [ ] **Step 1: Add `referral_code` field to register request**

In the `Register` handler, update the request struct:

```go
var req struct {
	PhoneNumber  string `json:"phone_number"`
	Name         string `json:"name"`
	ProfileURL   string `json:"profile_url"`
	NRCURL       string `json:"nrc_url"`
	ReferralCode string `json:"referral_code"` // optional
}
```

Update the `deService.Register` call:

```go
de, err := h.deService.Register(r.Context(), service.RegisterDERequest{
	PhoneNumber:  req.PhoneNumber,
	Name:         req.Name,
	ProfileURL:   req.ProfileURL,
	NRCURL:       req.NRCURL,
	ReferralCode: req.ReferralCode,
})
```

Update the success response to include `referral_code`:

```go
h.respondWithJSON(w, http.StatusCreated, map[string]interface{}{
	"de_id":         de.DEID,
	"phone_number":  de.PhoneNumber,
	"name":          de.Name,
	"status":        de.Status,
	"referral_code": de.ReferralCode,
	"created_at":    de.CreatedAt,
})
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./internal/handlers/...
```

Expected: no output

- [ ] **Step 3: Commit**

```bash
git add internal/handlers/de_handlers.go
git commit -m "feat: accept optional referral_code in DE register endpoint"
```

---

## Task 8: Referral Handler + Route

**Files:**
- Create: `internal/handlers/referral_handlers.go`
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Write the referral handler**

```go
// internal/handlers/referral_handlers.go
package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type ReferralHandlers struct {
	referralService *service.ReferralService
	logger          *logrus.Logger
}

func NewReferralHandlers(referralService *service.ReferralService, logger *logrus.Logger) *ReferralHandlers {
	return &ReferralHandlers{referralService: referralService, logger: logger}
}

// GET /api/v1/de/referral
// Returns the DE's referral code and the list of DEs they have referred with status.
func (h *ReferralHandlers) GetReferralScreen(w http.ResponseWriter, r *http.Request) {
	deID, _ := r.Context().Value("entity_id").(string)
	phone, _ := r.Context().Value("phone").(string)

	code, refs, err := h.referralService.GetReferralScreen(r.Context(), deID, phone)
	if err != nil {
		h.logger.WithError(err).Error("failed to get referral screen")
		h.respondWithError(w, http.StatusInternalServerError, "REFERRAL_FETCH_FAILED", "Failed to fetch referral details")
		return
	}

	type referralItem struct {
		ReferredDEID    string `json:"referred_de_id"`
		Status          string `json:"status"`
		CreatedAt       string `json:"created_at"`
		WindowExpiresAt string `json:"window_expires_at"`
		PayoutTriggeredAt string `json:"payout_triggered_at,omitempty"`
	}

	items := make([]referralItem, 0, len(refs))
	for _, ref := range refs {
		items = append(items, referralItem{
			ReferredDEID:      ref.ReferredDEID,
			Status:            string(ref.Status),
			CreatedAt:         ref.CreatedAt,
			WindowExpiresAt:   ref.WindowExpiresAt,
			PayoutTriggeredAt: ref.PayoutTriggeredAt,
		})
	}

	h.respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"referral_code": code,
		"referrals":     items,
	})
}

func (h *ReferralHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func (h *ReferralHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{
		Error: ErrorDetail{Code: code, Message: message},
	})
}
```

- [ ] **Step 2: Wire up in `main.go`**

In `main.go`, add to repository initialization:
```go
referralRepo := repository.NewReferralRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
payoutConfigRepo := repository.NewPayoutConfigRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
```

Add to service initialization:
```go
referralService := service.NewReferralService(referralRepo, deRepo, payoutConfigRepo, logger)
```

Update `deService` initialization (now requires `referralService`):
```go
deService := service.NewDEService(deRepo, qrService, referralService, logger)
```

Add handler:
```go
referralHandlers := handlers.NewReferralHandlers(referralService, logger)
```

Pass to `setupRouter` and add inside `deProtected` subrouter in `setupRouter`:
```go
deProtected.HandleFunc("/referral", referralHandlers.GetReferralScreen).Methods("GET", "OPTIONS")
```

- [ ] **Step 3: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./...
```

Expected: no output

- [ ] **Step 4: Commit**

```bash
git add internal/handlers/referral_handlers.go cmd/server/main.go
git commit -m "feat: add GET /de/referral endpoint and wire up referral system"
```

---

## Task 9: Config Update Handler (Payout Config PATCH)

**Files:**
- Create: `internal/handlers/config_handlers.go`
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Write the config handler**

```go
// internal/handlers/config_handlers.go
package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

type ConfigHandlers struct {
	payoutConfigRepo *repository.PayoutConfigRepository
	logger           *logrus.Logger
}

func NewConfigHandlers(payoutConfigRepo *repository.PayoutConfigRepository, logger *logrus.Logger) *ConfigHandlers {
	return &ConfigHandlers{payoutConfigRepo: payoutConfigRepo, logger: logger}
}

// PATCH /api/v1/config/payout
// Body: { "field": "referral_bonus_zmw", "value": "25.5" }
// Updates a single named field in the payout config. No auth required.
// Numeric fields are stored as DynamoDB Number type. String fields as String.
func (h *ConfigHandlers) UpdatePayoutConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Field string `json:"field"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if req.Field == "" || req.Value == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "field and value are required")
		return
	}

	// Attempt to parse as number first; fall back to string
	var attrValue types.AttributeValue
	if _, err := strconv.ParseFloat(req.Value, 64); err == nil {
		attrValue = &types.AttributeValueMemberN{Value: req.Value}
	} else {
		attrValue = &types.AttributeValueMemberS{Value: req.Value}
	}

	if err := h.payoutConfigRepo.UpdateField(r.Context(), req.Field, attrValue); err != nil {
		h.logger.WithError(err).Error("failed to update payout config")
		h.respondWithError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update config")
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]string{
		"field":  req.Field,
		"value":  req.Value,
		"status": "updated",
	})
}

func (h *ConfigHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func (h *ConfigHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{
		Error: ErrorDetail{Code: code, Message: message},
	})
}
```

- [ ] **Step 2: Wire into `main.go` and `setupRouter`**

Add handler init in `main.go`:
```go
configHandlers := handlers.NewConfigHandlers(payoutConfigRepo, logger)
```

Add to `setupRouter` under a new unprotected route (no auth):
```go
api.HandleFunc("/config/payout", configHandlers.UpdatePayoutConfig).Methods("PATCH", "OPTIONS")
```

- [ ] **Step 3: Build and verify**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./...
```

Expected: no output

- [ ] **Step 4: Commit**

```bash
git add internal/handlers/config_handlers.go cmd/server/main.go
git commit -m "feat: add PATCH /config/payout endpoint for runtime payout config updates"
```

---

## Task 10: DynamoDB GSI — ReferralCodeIndex

This task is an infrastructure step — the GSI must exist in DynamoDB before the referral lookup works.

- [ ] **Step 1: Add GSI to DynamoDB table**

If using local DynamoDB (development), update your table creation script or `docker-compose` to include the GSI. If using AWS, add via console or IaC.

GSI spec:
```
Index name:        ReferralCodeIndex
Partition key:     referral_code (String)
Sort key:          (none)
Projection:        ALL
Billing:           On-demand (matches main table)
```

For local DynamoDB via AWS CLI:
```bash
aws dynamodb update-table \
  --table-name QComTable \
  --attribute-definitions AttributeName=referral_code,AttributeType=S \
  --global-secondary-index-updates \
    '[{"Create":{"IndexName":"ReferralCodeIndex","KeySchema":[{"AttributeName":"referral_code","KeyType":"HASH"}],"Projection":{"ProjectionType":"ALL"}}}]' \
  --endpoint-url http://localhost:8000 \
  --region us-east-1
```

- [ ] **Step 2: Verify GSI is active**

```bash
aws dynamodb describe-table --table-name QComTable \
  --endpoint-url http://localhost:8000 --region us-east-1 \
  | grep -A5 ReferralCodeIndex
```

Expected: `"IndexStatus": "ACTIVE"`

- [ ] **Step 3: Commit infra note**

```bash
git commit --allow-empty -m "infra: ReferralCodeIndex GSI added to DynamoDB QComTable"
```

---

## Task 11: Integration Smoke Test

**Files:**
- Create: `tests/integration/referral_test.go`

- [ ] **Step 1: Write integration test**

```go
// tests/integration/referral_test.go
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// TestReferralFlow covers: register referrer → get referral code → register referred DE
// with the code → verify REFERRAL item exists → GET /de/referral returns the referred DE.
//
// Requires the server running at TEST_BASE_URL (default http://localhost:8080)
// and a seeded payout config with referral_window_days=30, referral_trips_threshold=5.
func TestReferralFlow(t *testing.T) {
	base := testBaseURL()

	// 1. Register referrer DE
	referrerPhone := uniquePhone()
	referrerBody := map[string]string{
		"phone_number": referrerPhone,
		"name":         "Referrer DE",
		"profile_url":  "https://example.com/p.jpg",
		"nrc_url":      "https://example.com/n.jpg",
	}
	referrerResp := mustPost(t, base+"/api/v1/de/register", referrerBody)
	if referrerResp["referral_code"] == nil {
		t.Fatal("expected referral_code in register response")
	}
	referralCode := referrerResp["referral_code"].(string)
	if len(referralCode) != 6 {
		t.Fatalf("expected 6-digit referral code, got %q", referralCode)
	}

	// 2. Register referred DE using the code
	referredPhone := uniquePhone()
	referredBody := map[string]string{
		"phone_number":  referredPhone,
		"name":          "Referred DE",
		"profile_url":   "https://example.com/p2.jpg",
		"nrc_url":       "https://example.com/n2.jpg",
		"referral_code": referralCode,
	}
	referredResp := mustPost(t, base+"/api/v1/de/register", referredBody)
	if referredResp["de_id"] == nil {
		t.Fatal("expected de_id in referred DE register response")
	}

	// 3. Auth as referrer, GET /de/referral — should show one active referral
	token := mustAuthDE(t, base, referrerPhone)
	req, _ := http.NewRequest("GET", base+"/api/v1/de/referral", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /de/referral failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var referralScreen map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&referralScreen)
	referrals := referralScreen["referrals"].([]interface{})
	if len(referrals) != 1 {
		t.Fatalf("expected 1 referral, got %d", len(referrals))
	}
	item := referrals[0].(map[string]interface{})
	if item["status"] != "active" {
		t.Fatalf("expected status=active, got %v", item["status"])
	}
	_ = referredResp
}

func testBaseURL() string {
	return "http://localhost:8080"
}

func uniquePhone() string {
	return fmt.Sprintf("+2609%08d", uniqueCounter())
}

var counter int

func uniqueCounter() int {
	counter++
	return counter
}

func mustPost(t *testing.T, url string, body interface{}) map[string]interface{} {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s failed: %v", url, err)
	}
	defer resp.Body.Close()
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	return result
}

func mustAuthDE(t *testing.T, base, phone string) string {
	t.Helper()
	// Initiate OTP
	mustPost(t, base+"/api/v1/auth/initiate-otp", map[string]string{
		"phone_number": phone,
		"app_type":     "de",
	})
	// Verify OTP with test code (requires IS_TEST=true on server)
	resp := mustPost(t, base+"/api/v1/auth/verify-otp", map[string]string{
		"phone_number": phone,
		"otp":          "000000",
		"app_type":     "de",
	})
	if resp["access_token"] == nil {
		t.Fatalf("auth failed for %s: %v", phone, resp)
	}
	return resp["access_token"].(string)
}
```

- [ ] **Step 2: Start server with test mode and run**

```bash
cd /Users/shivangawasthi/bunzo/qcom && IS_TEST=true go run cmd/server/main.go &
sleep 2
go test ./tests/integration/... -run TestReferralFlow -v
```

Expected:
```
--- PASS: TestReferralFlow
```

- [ ] **Step 3: Stop test server and commit**

```bash
kill %1
git add tests/integration/referral_test.go
git commit -m "test: add referral flow integration test"
```

---

## Plan Complete

**What this plan delivers:**
- Every new DE gets a unique 6-digit referral code at registration
- New DEs can optionally provide a referral code during registration
- `GET /de/referral` shows the DE their code and all DEs they've referred with status
- `PATCH /config/payout` lets ops update any payout config field at runtime
- `ReferralService.CheckAndTriggerBonus` is ready to be called from `TripService` (Plan A) at trip completion

**What Plan A must do to complete the referral bonus loop:**
After marking a trip as completed, `TripService` must call:
```go
bonusZMW, referrerDEID, err := referralService.CheckAndTriggerBonus(ctx, de.DEID, de.TotalTripsCompleted)
if bonusZMW > 0 {
    // add bonusZMW to both de.DEID and referrerDEID outstanding balances
    // write EarningsLedger entries of type "referral_bonus" for both DEs
}
```

**DynamoDB GSI required before production:**
- `ReferralCodeIndex` on DE table (PK: `referral_code`) — see Task 10
