# Payout & Earnings — Phase B3: Weekly Bonus Cron

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the weekly consistency bonus cron that fires every Monday at midnight Zambia time, scans all active DEs, counts qualifying working days for the previous week, computes the applicable bonus tier, writes a `DEWeeklySummary`, and appends a `weekly_bonus` ledger entry.

**Architecture:** A Go cron uses `time.AfterFunc` to fire at the next Monday midnight (Africa/Lusaka). It is fail-safe and idempotent — `WeeklySummaryRepository.Create` uses `attribute_not_exists` so re-running never double-credits. It processes DEs in batches. Uses the same `CronLock` key prefix as the assignment cron but with a distinct SK (`weekly-bonus`) to avoid conflicts.

**Tech Stack:** Go 1.24, AWS DynamoDB (aws-sdk-go-v2), logrus

**Prerequisites:**
- Phase A1 — `DERepository`, `timezone`, `CronLockRepository`
- Phase B1 — `WeeklySummaryRepository`, `EarningsLedgerRepository`
- Phase B2 — `PayoutService.WriteWeeklyBonusEntry`
- Plan C — `PayoutConfigRepository`

---

## File Map

### New Files
- `internal/service/weekly_bonus_cron.go` — weekly cron with Monday midnight scheduling

### Modified Files
- `cmd/server/main.go` — start weekly cron on server startup

---

## Task 1: Weekly Bonus Cron

**Files:**
- Create: `internal/service/weekly_bonus_cron.go`

- [ ] **Step 1: Write the cron**

```go
// internal/service/weekly_bonus_cron.go
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

const weeklyLockSK = "weekly-bonus"

type WeeklyBonusCron struct {
	deRepo             *repository.DERepository
	tripRepo           *repository.TripRepository
	weeklySummaryRepo  *repository.WeeklySummaryRepository
	earningsLedgerRepo *repository.EarningsLedgerRepository
	payoutConfigRepo   *repository.PayoutConfigRepository
	cronLockRepo       *repository.CronLockRepository
	logger             *logrus.Logger

	stopCh chan struct{}
}

func NewWeeklyBonusCron(
	deRepo *repository.DERepository,
	tripRepo *repository.TripRepository,
	weeklySummaryRepo *repository.WeeklySummaryRepository,
	earningsLedgerRepo *repository.EarningsLedgerRepository,
	payoutConfigRepo *repository.PayoutConfigRepository,
	cronLockRepo *repository.CronLockRepository,
	logger *logrus.Logger,
) *WeeklyBonusCron {
	return &WeeklyBonusCron{
		deRepo:             deRepo,
		tripRepo:           tripRepo,
		weeklySummaryRepo:  weeklySummaryRepo,
		earningsLedgerRepo: earningsLedgerRepo,
		payoutConfigRepo:   payoutConfigRepo,
		cronLockRepo:       cronLockRepo,
		logger:             logger,
		stopCh:             make(chan struct{}),
	}
}

// Start schedules the weekly cron to fire every Monday at midnight Zambia time.
func (c *WeeklyBonusCron) Start() {
	delay := c.durationUntilNextMondayMidnight()
	c.logger.WithField("delay_hours", delay.Hours()).
		Info("weekly bonus cron: scheduled for next Monday midnight Zambia time")

	go func() {
		select {
		case <-time.After(delay):
			c.runAndReschedule()
		case <-c.stopCh:
			return
		}
	}()
}

// Stop signals the cron to stop.
func (c *WeeklyBonusCron) Stop() {
	close(c.stopCh)
}

func (c *WeeklyBonusCron) runAndReschedule() {
	c.run()

	// Reschedule for next week
	go func() {
		select {
		case <-time.After(7 * 24 * time.Hour):
			c.runAndReschedule()
		case <-c.stopCh:
			return
		}
	}()
}

func (c *WeeklyBonusCron) run() {
	defer func() {
		if r := recover(); r != nil {
			c.logger.WithField("panic", r).Error("weekly bonus cron: panic recovered")
		}
	}()

	ctx := context.Background()
	c.logger.Info("weekly bonus cron: starting run")

	// Acquire a distinct weekly lock (SK = weekly-bonus)
	acquired, err := c.cronLockRepo.AcquireWithSK(ctx, weeklyLockSK, 3600) // 1-hour TTL
	if err != nil {
		c.logger.WithError(err).Error("weekly bonus cron: failed to acquire lock")
		return
	}
	if !acquired {
		c.logger.Info("weekly bonus cron: lock held by another instance, skipping")
		return
	}
	defer func() {
		_ = c.cronLockRepo.ReleaseWithSK(ctx, weeklyLockSK)
	}()

	cfg, err := c.payoutConfigRepo.Get(ctx)
	if err != nil {
		c.logger.WithError(err).Error("weekly bonus cron: failed to get payout config")
		return
	}

	// Compute previous week boundaries (Monday to Sunday, Zambia time)
	weekStart, weekEnd := c.previousWeekBounds()
	c.logger.WithFields(logrus.Fields{
		"week_start": weekStart, "week_end": weekEnd,
	}).Info("weekly bonus cron: processing week")

	// Scan all DEs — in production replace with a GSI or paginated scan
	des, err := c.scanAllDEs(ctx)
	if err != nil {
		c.logger.WithError(err).Error("weekly bonus cron: failed to scan DEs")
		return
	}

	processed, skipped := 0, 0
	for _, de := range des {
		if err := c.processDE(ctx, de, weekStart, weekEnd, cfg); err != nil {
			c.logger.WithError(err).WithField("de_id", de.DEID).
				Warn("weekly bonus cron: failed to process DE, skipping")
			skipped++
			continue
		}
		processed++
	}

	c.logger.WithFields(logrus.Fields{
		"processed": processed, "skipped": skipped,
	}).Info("weekly bonus cron: run complete")
}

// processDE counts qualifying working days for the DE in the previous week,
// computes the bonus tier, writes the summary (idempotent), and appends a ledger entry.
func (c *WeeklyBonusCron) processDE(ctx context.Context, de *models.DeliveryExecutive, weekStart, weekEnd string, cfg *models.PayoutConfig) error {
	// Count distinct working days — a day qualifies if DE completed >= MinDeliveriesPerDay trips
	daysWorked, totalTrips, err := c.countWorkingDays(ctx, de.DEID, weekStart, weekEnd, cfg.MinDeliveriesPerDay)
	if err != nil {
		return fmt.Errorf("failed to count working days: %w", err)
	}

	bonusZMW := computeWeeklyBonus(daysWorked, cfg)
	if bonusZMW == 0 {
		return nil // no bonus for this DE this week
	}

	// Write summary (idempotent — already_exists means cron already ran for this DE+week)
	summary := &models.DEWeeklySummary{
		DEID:           de.DEID,
		WeekStartDate:  weekStart,
		WeekEndDate:    weekEnd,
		DaysWorked:     daysWorked,
		TripsCompleted: totalTrips,
		BonusAmountZMW: bonusZMW,
		Status:         "computed",
	}
	if err := c.weeklySummaryRepo.Create(ctx, summary); err != nil {
		if isAlreadyExists(err) {
			return nil // idempotent — already processed
		}
		return fmt.Errorf("failed to write weekly summary: %w", err)
	}

	// Write ledger entry
	entry := &models.EarningsLedger{
		DEID:        de.DEID,
		EarningID:   uuid.New().String(),
		Type:        models.EarningTypeWeeklyBonus,
		AmountZMW:   bonusZMW,
		CreatedAt:   timezone.Now().Format(time.RFC3339),
		ReferenceID: weekStart,
	}
	if err := c.earningsLedgerRepo.Append(ctx, entry); err != nil {
		return fmt.Errorf("failed to write ledger entry: %w", err)
	}

	c.logger.WithFields(logrus.Fields{
		"de_id": de.DEID, "days_worked": daysWorked, "bonus_zmw": bonusZMW,
	}).Info("weekly bonus cron: bonus credited")
	return nil
}

// countWorkingDays queries DETripsIndex for the week and counts distinct days
// where the DE completed >= minDeliveriesPerDay trips.
func (c *WeeklyBonusCron) countWorkingDays(ctx context.Context, deID, weekStart, weekEnd string, minDeliveries int) (int, int, error) {
	// Query all completed trips in the week window
	trips, _, err := c.tripRepo.ListByDEAfter(ctx, deID, weekStart+"T00:00:00+02:00", 200, nil)
	if err != nil {
		return 0, 0, err
	}

	// Group by date, count trips per day
	dayCount := make(map[string]int)
	total := 0
	for _, trip := range trips {
		if trip.CompletedAt == "" || trip.CompletedAt > weekEnd+"T23:59:59+02:00" {
			continue
		}
		t, err := time.Parse(time.RFC3339, trip.CompletedAt)
		if err != nil {
			continue
		}
		date := t.In(timezone.ZambiaLocation()).Format("2006-01-02")
		dayCount[date]++
		total++
	}

	daysWorked := 0
	for _, count := range dayCount {
		if count >= minDeliveries {
			daysWorked++
		}
	}
	return daysWorked, total, nil
}

// computeWeeklyBonus returns the weekly bonus for the given days worked.
func computeWeeklyBonus(daysWorked int, cfg *models.PayoutConfig) float64 {
	switch {
	case daysWorked >= cfg.WeeklyW3Days:
		return cfg.WeeklyW3BonusZMW
	case daysWorked >= cfg.WeeklyW2Days:
		return cfg.WeeklyW2BonusZMW
	case daysWorked >= cfg.WeeklyW1Days:
		return cfg.WeeklyW1BonusZMW
	default:
		return 0
	}
}

// previousWeekBounds returns (weekStart, weekEnd) date strings for the
// Monday-to-Sunday week that just ended, in Africa/Lusaka timezone.
func (c *WeeklyBonusCron) previousWeekBounds() (string, string) {
	now := timezone.Now()
	// Go back 7 days to reach last week, then find that Monday
	lastWeek := now.AddDate(0, 0, -7)
	weekday := int(lastWeek.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	monday := lastWeek.AddDate(0, 0, -(weekday - 1))
	sunday := monday.AddDate(0, 0, 6)
	return monday.Format("2006-01-02"), sunday.Format("2006-01-02")
}

// durationUntilNextMondayMidnight returns the duration from now until
// the next Monday at 00:00:00 Africa/Lusaka.
func (c *WeeklyBonusCron) durationUntilNextMondayMidnight() time.Duration {
	loc := timezone.ZambiaLocation()
	now := time.Now().In(loc)
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	daysUntilMonday := (8 - weekday) % 7
	if daysUntilMonday == 0 {
		daysUntilMonday = 7
	}
	nextMonday := time.Date(now.Year(), now.Month(), now.Day()+daysUntilMonday, 0, 0, 0, 0, loc)
	return time.Until(nextMonday)
}

// scanAllDEs performs a table scan to retrieve all DE records.
// For large DE counts, replace with a paginated scan or a GSI-backed query.
func (c *WeeklyBonusCron) scanAllDEs(ctx context.Context) ([]*models.DeliveryExecutive, error) {
	// Re-use FindEligibleByStore pattern — for all DEs we need a full scan
	// filtered by begins_with(PK, "DE!"). This is acceptable at current scale.
	return c.deRepo.ScanAll(ctx)
}

// isAlreadyExists checks if an error indicates a DynamoDB conditional check failure
// (i.e. the item already exists — idempotency guard).
func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	return contains(err.Error(), "already exists")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Add `AcquireWithSK`, `ReleaseWithSK`, and `ScanAll` methods**

The weekly cron needs a separate lock key (SK = `weekly-bonus`) and a `ScanAll` on the DE repo.

Add to `internal/repository/cron_lock_repository.go`:

```go
// AcquireWithSK acquires a lock with a custom SK (allows multiple independent crons).
func (r *CronLockRepository) AcquireWithSK(ctx context.Context, sk string, ttlSeconds int) (bool, error) {
	op := logging.Start(ctx, r.logger, "CronLock.AcquireWithSK", logrus.Fields{"sk": sk})
	defer op.End()

	expiresAt := time.Now().UTC().Add(time.Duration(ttlSeconds) * time.Second).Format(time.RFC3339)

	_, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item: map[string]types.AttributeValue{
			"PK":         &types.AttributeValueMemberS{Value: cronLockPK},
			"SK":         &types.AttributeValueMemberS{Value: sk},
			"expires_at": &types.AttributeValueMemberS{Value: expiresAt},
		},
		ConditionExpression: aws.String("attribute_not_exists(PK) OR expires_at < :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":now": &types.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339)},
		},
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return false, nil
		}
		return false, op.Fail(fmt.Errorf("failed to acquire weekly lock: %w", err))
	}
	return true, nil
}

// ReleaseWithSK releases a lock with a custom SK.
func (r *CronLockRepository) ReleaseWithSK(ctx context.Context, sk string) error {
	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: cronLockPK},
			"SK": &types.AttributeValueMemberS{Value: sk},
		},
	})
	return err
}
```

Add to `internal/repository/de_repository.go`:

```go
// ScanAll returns all DeliveryExecutive records via a table scan filtered by PK prefix.
// Used by the weekly bonus cron. For large tables add a GSI on entity_type.
func (r *DERepository) ScanAll(ctx context.Context) ([]*models.DeliveryExecutive, error) {
	op := logging.Start(ctx, r.logger, "DERepository.ScanAll", nil)
	defer op.End()

	var des []*models.DeliveryExecutive
	var lastKey map[string]types.AttributeValue

	for {
		result, err := r.client.Scan(ctx, &dynamodb.ScanInput{
			TableName:        aws.String(r.tableName),
			FilterExpression: aws.String("begins_with(PK, :prefix) AND SK = :meta"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":prefix": &types.AttributeValueMemberS{Value: "DE!"},
				":meta":   &types.AttributeValueMemberS{Value: "METADATA"},
			},
			ExclusiveStartKey: lastKey,
		})
		if err != nil {
			return nil, op.Fail(fmt.Errorf("failed to scan DEs: %w", err))
		}
		for _, item := range result.Items {
			var de models.DeliveryExecutive
			if err := attributevalue.UnmarshalMap(item, &de); err != nil {
				continue
			}
			des = append(des, &de)
		}
		if result.LastEvaluatedKey == nil {
			break
		}
		lastKey = result.LastEvaluatedKey
	}

	op.With("count", len(des))
	return des, nil
}
```

- [ ] **Step 3: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./...
```

Expected: no output

- [ ] **Step 4: Commit**

```bash
git add internal/service/weekly_bonus_cron.go internal/repository/cron_lock_repository.go internal/repository/de_repository.go
git commit -m "feat: add WeeklyBonusCron with Monday midnight scheduling, idempotent bonus writes, and distributed lock"
```

---

## Task 2: Wire Weekly Cron into main.go

**Files:**
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Add weekly cron to main.go**

Add to service initialization:
```go
weeklyBonusCron := service.NewWeeklyBonusCron(
	deRepo,
	tripRepo,
	weeklySummaryRepo,
	earningsLedgerRepo,
	payoutConfigRepo,
	cronLockRepo,
	logger,
)
```

Start after server starts:
```go
weeklyBonusCron.Start()
```

Stop before shutdown:
```go
logger.Info("Stopping weekly bonus cron...")
weeklyBonusCron.Stop()
```

Add repo init:
```go
weeklySummaryRepo := repository.NewWeeklySummaryRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
```

- [ ] **Step 2: Build the full server**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./...
```

Expected: no output

- [ ] **Step 3: Commit**

```bash
git add cmd/server/main.go
git commit -m "feat: start weekly bonus cron on server startup with graceful shutdown"
```

---

## Task 3: Weekly Bonus Unit Tests

**Files:**
- Create: `internal/service/weekly_bonus_cron_test.go`

- [ ] **Step 1: Write tests**

```go
// internal/service/weekly_bonus_cron_test.go
package service

import (
	"testing"

	"github.com/qcom/qcom/internal/models"
)

func TestComputeWeeklyBonus_BelowThreshold(t *testing.T) {
	cfg := &models.PayoutConfig{
		WeeklyW1Days: 5, WeeklyW1BonusZMW: 150,
		WeeklyW2Days: 6, WeeklyW2BonusZMW: 250,
		WeeklyW3Days: 7, WeeklyW3BonusZMW: 400,
	}
	if computeWeeklyBonus(4, cfg) != 0 {
		t.Fatal("expected 0 bonus for 4 days worked")
	}
}

func TestComputeWeeklyBonus_W1(t *testing.T) {
	cfg := &models.PayoutConfig{
		WeeklyW1Days: 5, WeeklyW1BonusZMW: 150,
		WeeklyW2Days: 6, WeeklyW2BonusZMW: 250,
		WeeklyW3Days: 7, WeeklyW3BonusZMW: 400,
	}
	if computeWeeklyBonus(5, cfg) != 150 {
		t.Fatal("expected W1 bonus for 5 days")
	}
}

func TestComputeWeeklyBonus_W2(t *testing.T) {
	cfg := &models.PayoutConfig{
		WeeklyW1Days: 5, WeeklyW1BonusZMW: 150,
		WeeklyW2Days: 6, WeeklyW2BonusZMW: 250,
		WeeklyW3Days: 7, WeeklyW3BonusZMW: 400,
	}
	if computeWeeklyBonus(6, cfg) != 250 {
		t.Fatal("expected W2 bonus for 6 days")
	}
}

func TestComputeWeeklyBonus_W3(t *testing.T) {
	cfg := &models.PayoutConfig{
		WeeklyW1Days: 5, WeeklyW1BonusZMW: 150,
		WeeklyW2Days: 6, WeeklyW2BonusZMW: 250,
		WeeklyW3Days: 7, WeeklyW3BonusZMW: 400,
	}
	if computeWeeklyBonus(7, cfg) != 400 {
		t.Fatal("expected W3 bonus for 7 days")
	}
}
```

- [ ] **Step 2: Run tests**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go test ./internal/service/... -run "TestComputeWeeklyBonus" -v
```

Expected: all 4 tests pass

- [ ] **Step 3: Commit**

```bash
git add internal/service/weekly_bonus_cron_test.go
git commit -m "test: add weekly bonus tier computation unit tests"
```

---

## Phase B3 Complete

**What this phase delivers:**
- `WeeklyBonusCron` — fires every Monday at midnight Africa/Lusaka, idempotent, distributed-safe
- Counts qualifying working days (≥ `min_deliveries_per_day` trips per day)
- Writes `DEWeeklySummary` and `EarningsLedger` (type: `weekly_bonus`) for each eligible DE
- `CronLockRepository` extended with `AcquireWithSK`/`ReleaseWithSK` for independent lock keys
- `DERepository` extended with `ScanAll`

**Phase B4 picks up here** by exposing the populated ledger via the earnings APIs.
