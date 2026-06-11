# Payout & Earnings — Phase B2: Payout Service

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `PayoutService` which computes base pay (at trip creation) and bonus pay (at trip completion), then writes `EarningsLedger` entries. Hook it into `TripService.completeDelivery` so every completed trip automatically generates ledger entries for the DE.

**Architecture:** `PayoutService` reads `PayoutConfig` from DynamoDB once per computation and applies the rate formula. At trip completion it computes the tier bonus using `DE.DailyTripCount`, writes a `trip` ledger entry, and if a referral bonus was triggered writes `referral_bonus` entries for both DEs. `TripService.completeDelivery` calls `PayoutService.OnTripCompleted` in the same goroutine.

**Tech Stack:** Go 1.24, AWS DynamoDB (aws-sdk-go-v2), logrus

**Prerequisites:**
- Phase A1 — `TripRepository`, `DERepository.IncrementDailyCount`, `timezone`
- Phase A3 — `TripService.completeDelivery` exists
- Phase B1 — `EarningsLedgerRepository`
- Plan C — `PayoutConfigRepository`, `ReferralService.CheckAndTriggerBonus`

---

## File Map

### New Files
- `internal/service/payout_service.go` — base pay + tier bonus computation + ledger writes
- `internal/service/payout_service_test.go` — unit tests for payout formulas

### Modified Files
- `internal/service/trip_service.go` — call `PayoutService.OnTripCompleted` in `completeDelivery`
- `cmd/server/main.go` — wire PayoutService

---

## Task 1: Payout Service

**Files:**
- Create: `internal/service/payout_service_test.go`
- Create: `internal/service/payout_service.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/service/payout_service_test.go
package service

import (
	"testing"

	"github.com/qcom/qcom/internal/models"
)

func TestComputeBasePay(t *testing.T) {
	cfg := &models.PayoutConfig{RatePerKmZMW: 5.0}
	pay := computeBasePay(3.0, cfg)
	if pay != 15.0 {
		t.Fatalf("expected 15.0 ZMW for 3km at 5/km, got %.2f", pay)
	}
}

func TestComputeBasePay_ZeroDistance(t *testing.T) {
	cfg := &models.PayoutConfig{RatePerKmZMW: 5.0}
	pay := computeBasePay(0, cfg)
	if pay != 0 {
		t.Fatalf("expected 0 ZMW for 0km, got %.2f", pay)
	}
}

func TestComputeTierBonus_BelowTier1(t *testing.T) {
	cfg := &models.PayoutConfig{
		Tier1Threshold: 10, Tier1BonusZMW: 10,
		Tier2Threshold: 15, Tier2BonusZMW: 20,
	}
	bonus := computeTierBonus(5, cfg)
	if bonus != 0 {
		t.Fatalf("expected 0 bonus for delivery 5, got %.2f", bonus)
	}
}

func TestComputeTierBonus_AtTier1(t *testing.T) {
	cfg := &models.PayoutConfig{
		Tier1Threshold: 10, Tier1BonusZMW: 10,
		Tier2Threshold: 15, Tier2BonusZMW: 20,
	}
	bonus := computeTierBonus(11, cfg)
	if bonus != 10 {
		t.Fatalf("expected 10 ZMW tier1 bonus at delivery 11, got %.2f", bonus)
	}
}

func TestComputeTierBonus_AtTier2(t *testing.T) {
	cfg := &models.PayoutConfig{
		Tier1Threshold: 10, Tier1BonusZMW: 10,
		Tier2Threshold: 15, Tier2BonusZMW: 20,
	}
	bonus := computeTierBonus(16, cfg)
	if bonus != 20 {
		t.Fatalf("expected 20 ZMW tier2 bonus at delivery 16, got %.2f", bonus)
	}
}

func TestComputeTierBonus_ExactTier1Boundary(t *testing.T) {
	cfg := &models.PayoutConfig{
		Tier1Threshold: 10, Tier1BonusZMW: 10,
		Tier2Threshold: 15, Tier2BonusZMW: 20,
	}
	// delivery 10 = baseline (no bonus), delivery 11 = tier1
	if computeTierBonus(10, cfg) != 0 {
		t.Fatal("delivery 10 should have no bonus (threshold is >10)")
	}
	if computeTierBonus(11, cfg) != 10 {
		t.Fatal("delivery 11 should earn tier1 bonus")
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go test ./internal/service/... -run "TestComputeBasePay|TestComputeTierBonus" -v
```

Expected: `FAIL` — functions not defined

- [ ] **Step 3: Write the payout service**

```go
// internal/service/payout_service.go
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

type PayoutService struct {
	payoutConfigRepo  *repository.PayoutConfigRepository
	earningsLedgerRepo *repository.EarningsLedgerRepository
	deRepo            *repository.DERepository
	tripRepo          *repository.TripRepository
	referralService   *ReferralService
	logger            *logrus.Logger
}

func NewPayoutService(
	payoutConfigRepo *repository.PayoutConfigRepository,
	earningsLedgerRepo *repository.EarningsLedgerRepository,
	deRepo *repository.DERepository,
	tripRepo *repository.TripRepository,
	referralService *ReferralService,
	logger *logrus.Logger,
) *PayoutService {
	return &PayoutService{
		payoutConfigRepo:   payoutConfigRepo,
		earningsLedgerRepo: earningsLedgerRepo,
		deRepo:             deRepo,
		tripRepo:           tripRepo,
		referralService:    referralService,
		logger:             logger,
	}
}

// ComputeBasePayZMW returns the base pay for a trip at creation time.
// Called by the assignment cron — does not write to DynamoDB.
func (s *PayoutService) ComputeBasePayZMW(ctx context.Context, distanceKM float64) (float64, error) {
	cfg, err := s.payoutConfigRepo.Get(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get payout config: %w", err)
	}
	return computeBasePay(distanceKM, cfg), nil
}

// OnTripCompleted is called by TripService when a trip's drop task is completed.
// It computes the tier bonus, stamps it on the trip, writes ledger entries,
// and checks for referral bonus triggers.
// dePhone is the assigned DE's phone number.
func (s *PayoutService) OnTripCompleted(ctx context.Context, trip *models.Trip, dePhone string) {
	op := logging.Start(ctx, s.logger, "PayoutService.OnTripCompleted", logrus.Fields{
		"trip_id": trip.TripID, "de_phone": dePhone,
	})
	defer op.End()

	cfg, err := s.payoutConfigRepo.Get(ctx)
	if err != nil {
		s.logger.WithError(err).Error("payout: failed to get config on trip completion")
		return
	}

	de, err := s.deRepo.GetByPhone(ctx, dePhone)
	if err != nil || de == nil {
		s.logger.WithError(err).Error("payout: failed to fetch DE on trip completion")
		return
	}

	// Increment daily count — returns new count (post-increment)
	newDailyCount, err := s.deRepo.IncrementDailyCount(ctx, dePhone, timezone.DateString())
	if err != nil {
		s.logger.WithError(err).Error("payout: failed to increment daily count")
		return
	}

	bonusPayZMW := computeTierBonus(newDailyCount, cfg)
	totalPayZMW := trip.BasePayZMW + bonusPayZMW

	// Stamp bonus + total on the trip record
	if err := s.tripRepo.UpdatePayout(ctx, trip.TripID,
		trip.DistanceKM, trip.BasePayZMW, bonusPayZMW, totalPayZMW, newDailyCount); err != nil {
		s.logger.WithError(err).Error("payout: failed to update trip payout fields")
	}

	// Write trip earning to ledger
	now := timezone.Now().Format(time.RFC3339)
	tripEntry := &models.EarningsLedger{
		DEID:        de.DEID,
		EarningID:   uuid.New().String(),
		Type:        models.EarningTypeTrip,
		AmountZMW:   totalPayZMW,
		CreatedAt:   now,
		ReferenceID: trip.TripID,
	}
	if err := s.earningsLedgerRepo.Append(ctx, tripEntry); err != nil {
		s.logger.WithError(err).Error("payout: failed to write trip ledger entry")
	}

	// Check referral bonus — TotalTripsCompleted was incremented by IncrementDailyCount
	// Fetch updated DE to get the new TotalTripsCompleted
	updatedDE, err := s.deRepo.GetByPhone(ctx, dePhone)
	if err == nil && updatedDE != nil && s.referralService != nil {
		bonusZMW, referrerDEID, err := s.referralService.CheckAndTriggerBonus(
			ctx, updatedDE.DEID, updatedDE.TotalTripsCompleted)
		if err != nil {
			s.logger.WithError(err).Warn("payout: referral bonus check failed")
		} else if bonusZMW > 0 {
			s.writeReferralBonusEntries(ctx, updatedDE.DEID, referrerDEID, bonusZMW, now)
		}
	}
}

// WriteWeeklyBonusEntry writes a weekly consistency bonus ledger entry for a DE.
// Called by the weekly cron after computing the bonus.
func (s *PayoutService) WriteWeeklyBonusEntry(ctx context.Context, deID, weekStartDate string, bonusZMW float64) error {
	entry := &models.EarningsLedger{
		DEID:        deID,
		EarningID:   uuid.New().String(),
		Type:        models.EarningTypeWeeklyBonus,
		AmountZMW:   bonusZMW,
		CreatedAt:   timezone.Now().Format(time.RFC3339),
		ReferenceID: weekStartDate,
	}
	return s.earningsLedgerRepo.Append(context.Background(), entry)
}

func (s *PayoutService) writeReferralBonusEntries(ctx context.Context, referredDEID, referrerDEID string, bonusZMW float64, now string) {
	for _, deID := range []string{referredDEID, referrerDEID} {
		entry := &models.EarningsLedger{
			DEID:        deID,
			EarningID:   uuid.New().String(),
			Type:        models.EarningTypeReferralBonus,
			AmountZMW:   bonusZMW,
			CreatedAt:   now,
			ReferenceID: referredDEID,
		}
		if err := s.earningsLedgerRepo.Append(ctx, entry); err != nil {
			s.logger.WithError(err).WithField("de_id", deID).
				Error("payout: failed to write referral bonus ledger entry")
		}
	}
}

// computeBasePay returns x * distance_km.
func computeBasePay(distanceKM float64, cfg *models.PayoutConfig) float64 {
	return cfg.RatePerKmZMW * distanceKM
}

// computeTierBonus returns the per-delivery bonus based on daily delivery rank.
// Rank is 1-indexed: delivery 1 = first delivery of the day.
func computeTierBonus(dailyRank int, cfg *models.PayoutConfig) float64 {
	switch {
	case dailyRank > cfg.Tier2Threshold:
		return cfg.Tier2BonusZMW
	case dailyRank > cfg.Tier1Threshold:
		return cfg.Tier1BonusZMW
	default:
		return 0
	}
}
```

- [ ] **Step 4: Run tests — expect pass**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go test ./internal/service/... -run "TestComputeBasePay|TestComputeTierBonus" -v
```

Expected: all 5 tests pass

- [ ] **Step 5: Commit**

```bash
git add internal/service/payout_service.go internal/service/payout_service_test.go
git commit -m "feat: add PayoutService with base pay + tier bonus computation and ledger writes"
```

---

## Task 2: Hook PayoutService into TripService

**Files:**
- Modify: `internal/service/trip_service.go`

- [ ] **Step 1: Add PayoutService dependency to TripService**

Update `TripService` struct and constructor:

```go
type TripService struct {
	tripRepo      *repository.TripRepository
	deRepo        *repository.DERepository
	javaClient    *JavaOrderClient
	payoutService *PayoutService
	logger        *logrus.Logger
}

func NewTripService(
	tripRepo *repository.TripRepository,
	deRepo *repository.DERepository,
	javaClient *JavaOrderClient,
	payoutService *PayoutService,
	logger *logrus.Logger,
) *TripService {
	return &TripService{
		tripRepo:      tripRepo,
		deRepo:        deRepo,
		javaClient:    javaClient,
		payoutService: payoutService,
		logger:        logger,
	}
}
```

- [ ] **Step 2: Call PayoutService in completeDelivery**

In `internal/service/trip_service.go`, update `completeDelivery`:

```go
func (s *TripService) completeDelivery(trip *models.Trip, de *models.DeliveryExecutive) {
	ctx := context.Background()

	// Payout computation + ledger write (handles daily count increment internally)
	if s.payoutService != nil {
		s.payoutService.OnTripCompleted(ctx, trip, de.PhoneNumber)
	}

	// Free the DE
	if err := s.deRepo.UpdateStatus(ctx, de.PhoneNumber, models.DEStatusFree, "", ""); err != nil {
		s.logger.WithError(err).WithField("de_phone", de.PhoneNumber).
			Error("failed to set DE free after trip completion")
	}
}
```

**Note:** `IncrementDailyCount` is now called inside `PayoutService.OnTripCompleted` — remove any existing call to it from `completeDelivery` to avoid double increment.

- [ ] **Step 3: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./...
```

Expected: no output

- [ ] **Step 4: Commit**

```bash
git add internal/service/trip_service.go
git commit -m "feat: hook PayoutService into TripService.completeDelivery for payout on trip completion"
```

---

## Task 3: Update main.go

**Files:**
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Wire PayoutService and update TripService constructor**

Add to repository initialization:
```go
earningsLedgerRepo := repository.NewEarningsLedgerRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
```

Add to service initialization:
```go
payoutService := service.NewPayoutService(
	payoutConfigRepo,
	earningsLedgerRepo,
	deRepo,
	tripRepo,
	referralService,
	logger,
)
```

Update `tripService` initialization (now requires `payoutService`):
```go
tripService := service.NewTripService(tripRepo, deRepo, javaOrderClient, payoutService, logger)
```

- [ ] **Step 2: Build the full server**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./...
```

Expected: no output

- [ ] **Step 3: Commit**

```bash
git add cmd/server/main.go
git commit -m "feat: wire PayoutService and EarningsLedgerRepository into server"
```

---

## Task 4: Payout Unit Tests — Edge Cases

**Files:**
- Modify: `internal/service/payout_service_test.go`

- [ ] **Step 1: Add edge case tests**

Append to `internal/service/payout_service_test.go`:

```go
func TestComputeTierBonus_ExactTier2Boundary(t *testing.T) {
	cfg := &models.PayoutConfig{
		Tier1Threshold: 10, Tier1BonusZMW: 10,
		Tier2Threshold: 15, Tier2BonusZMW: 20,
	}
	if computeTierBonus(15, cfg) != 10 {
		t.Fatal("delivery 15 should earn tier1 bonus (threshold is >15 for tier2)")
	}
	if computeTierBonus(16, cfg) != 20 {
		t.Fatal("delivery 16 should earn tier2 bonus")
	}
}

func TestComputeBasePay_FractionalDistance(t *testing.T) {
	cfg := &models.PayoutConfig{RatePerKmZMW: 5.0}
	pay := computeBasePay(2.5, cfg)
	if pay != 12.5 {
		t.Fatalf("expected 12.5 ZMW for 2.5km at 5/km, got %.2f", pay)
	}
}

func TestComputeTierBonus_ZeroThresholds(t *testing.T) {
	// If config not yet set (zero values), no bonus should be awarded
	cfg := &models.PayoutConfig{}
	bonus := computeTierBonus(5, cfg)
	// dailyRank=5 > Tier2Threshold=0, so it returns Tier2BonusZMW=0
	if bonus != 0 {
		t.Fatalf("expected 0 bonus with zero config, got %.2f", bonus)
	}
}
```

- [ ] **Step 2: Run all payout tests**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go test ./internal/service/... -run "TestComputeBasePay|TestComputeTierBonus" -v
```

Expected: all 8 tests pass

- [ ] **Step 3: Commit**

```bash
git add internal/service/payout_service_test.go
git commit -m "test: add edge case tests for payout tier boundary conditions"
```

---

## Phase B2 Complete

**What this phase delivers:**
- `PayoutService` with `ComputeBasePayZMW`, `OnTripCompleted`, `WriteWeeklyBonusEntry`
- Tier bonus computed from `DailyTripCount` at trip completion
- `EarningsLedger` entries written at trip completion for `trip` and `referral_bonus` types
- `TripService.completeDelivery` now calls payout before freeing the DE

**Phase B3 picks up here** by implementing the weekly cron that calls `WriteWeeklyBonusEntry`.
**Phase B4 picks up here** by querying the populated ledger for the earnings screen.
