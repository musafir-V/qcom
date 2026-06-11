# Payout & Earnings — Phase B1: Data Layer

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Define the EarningsLedger, WeeklySummary, and Disbursement DynamoDB entities and their repositories. After this phase, all persistence building blocks for the payout system are in place and independently testable.

**Architecture:** Three new DynamoDB item types share the single QComTable using key prefixes. `EarningsLedger` (`EARN!{deId}` PK, `{created_at}#{earning_id}` SK) is the unified sorted list of all earning events. `WeeklySummary` (`WEEKLY!{deId}` PK, `{weekStartDate}` SK) stores the weekly consistency bonus per DE per week. `Disbursement` (`DISBURSEMENT!{deId}` PK, `{disbursedAt}#{disbursementId}` SK) records each offline payout event.

**Tech Stack:** Go 1.24, AWS DynamoDB (aws-sdk-go-v2), logrus, uuid

**Prerequisites:** None — this phase is fully independent.

---

## File Map

### New Files
- `internal/models/earnings_ledger.go` — EarningsLedger model + earning type constants
- `internal/models/weekly_summary.go` — DEWeeklySummary model
- `internal/models/disbursement.go` — Disbursement model
- `internal/repository/earnings_ledger_repository.go` — append + paginated query
- `internal/repository/weekly_summary_repository.go` — create + get by week
- `internal/repository/disbursement_repository.go` — create + list by DE

---

## Task 1: Models

**Files:**
- Create: `internal/models/earnings_ledger.go`
- Create: `internal/models/weekly_summary.go`
- Create: `internal/models/disbursement.go`

- [ ] **Step 1: Write EarningsLedger model**

```go
// internal/models/earnings_ledger.go
package models

type EarningType string

const (
	EarningTypeTrip          EarningType = "trip"
	EarningTypeWeeklyBonus   EarningType = "weekly_bonus"
	EarningTypeReferralBonus EarningType = "referral_bonus"
)

// EarningsLedger is one earning event for a DE.
// PK = EARN!{deId}, SK = {created_at}#{earning_id}
// Sorted by SK ascending gives chronological order.
type EarningsLedger struct {
	DEID       string      `json:"de_id" dynamodbav:"de_id"`
	EarningID  string      `json:"earning_id" dynamodbav:"earning_id"`
	Type       EarningType `json:"type" dynamodbav:"type"`
	AmountZMW  float64     `json:"amount_zmw" dynamodbav:"amount_zmw"`
	CreatedAt  string      `json:"created_at" dynamodbav:"created_at"` // Zambia timezone RFC3339
	ReferenceID string     `json:"reference_id" dynamodbav:"reference_id"` // trip_id | week_start | referred_de_id
}

func (e *EarningsLedger) GetPK() string { return "EARN!" + e.DEID }
func (e *EarningsLedger) GetSK() string { return e.CreatedAt + "#" + e.EarningID }
```

- [ ] **Step 2: Write WeeklySummary model**

```go
// internal/models/weekly_summary.go
package models

// DEWeeklySummary records the weekly consistency bonus for a DE for one week.
// PK = WEEKLY!{deId}, SK = {weekStartDate} (e.g. "2026-06-01")
type DEWeeklySummary struct {
	DEID          string  `json:"de_id" dynamodbav:"de_id"`
	WeekStartDate string  `json:"week_start_date" dynamodbav:"week_start_date"`
	WeekEndDate   string  `json:"week_end_date" dynamodbav:"week_end_date"`
	DaysWorked    int     `json:"days_worked" dynamodbav:"days_worked"`
	TripsCompleted int    `json:"trips_completed" dynamodbav:"trips_completed"`
	BonusAmountZMW float64 `json:"bonus_amount_zmw" dynamodbav:"bonus_amount_zmw"`
	Status        string  `json:"status" dynamodbav:"status"` // "computed" | "paid"
	CreatedAt     string  `json:"created_at" dynamodbav:"created_at"`
}

func (w *DEWeeklySummary) GetPK() string { return "WEEKLY!" + w.DEID }
func (w *DEWeeklySummary) GetSK() string { return w.WeekStartDate }
```

- [ ] **Step 3: Write Disbursement model**

```go
// internal/models/disbursement.go
package models

// Disbursement records an offline payout from Bunzo to a DE.
// PK = DISBURSEMENT!{deId}, SK = {disbursedAt}#{disbursementId}
type Disbursement struct {
	DEID           string  `json:"de_id" dynamodbav:"de_id"`
	DisbursementID string  `json:"disbursement_id" dynamodbav:"disbursement_id"`
	AmountZMW      float64 `json:"amount_zmw" dynamodbav:"amount_zmw"`
	PeriodFrom     string  `json:"period_from" dynamodbav:"period_from"`
	PeriodTo       string  `json:"period_to" dynamodbav:"period_to"`
	DisbursedAt    string  `json:"disbursed_at" dynamodbav:"disbursed_at"`
}

func (d *Disbursement) GetPK() string { return "DISBURSEMENT!" + d.DEID }
func (d *Disbursement) GetSK() string { return d.DisbursedAt + "#" + d.DisbursementID }
```

- [ ] **Step 4: Verify all models compile**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./internal/models/...
```

Expected: no output

- [ ] **Step 5: Commit**

```bash
git add internal/models/earnings_ledger.go internal/models/weekly_summary.go internal/models/disbursement.go
git commit -m "feat: add EarningsLedger, DEWeeklySummary, Disbursement models"
```

---

## Task 2: EarningsLedger Repository

**Files:**
- Create: `internal/repository/earnings_ledger_repository.go`

- [ ] **Step 1: Write the repository**

```go
// internal/repository/earnings_ledger_repository.go
package repository

import (
	"context"
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

type EarningsLedgerRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewEarningsLedgerRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *EarningsLedgerRepository {
	return &EarningsLedgerRepository{client: client, tableName: tableName, logger: logger}
}

// Append writes a new earning entry to the ledger.
func (r *EarningsLedgerRepository) Append(ctx context.Context, entry *models.EarningsLedger) error {
	op := logging.Start(ctx, r.logger, "EarningsLedger.Append", logrus.Fields{
		"de_id": entry.DEID, "type": string(entry.Type), "amount": entry.AmountZMW,
	})
	defer op.End()

	if entry.CreatedAt == "" {
		entry.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	item, err := attributevalue.MarshalMap(entry)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal ledger entry: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: entry.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: entry.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to append ledger entry: %w", err))
	}
	return nil
}

// QueryByDE returns earning entries for a DE sorted by created_at descending (newest first).
// Pass afterTimestamp="" to get all entries. Pass lastKey for pagination.
// pageSize max 50.
func (r *EarningsLedgerRepository) QueryByDE(ctx context.Context, deID, afterTimestamp string, pageSize int32, lastKey map[string]types.AttributeValue) ([]*models.EarningsLedger, map[string]types.AttributeValue, error) {
	op := logging.Start(ctx, r.logger, "EarningsLedger.QueryByDE", logrus.Fields{"de_id": deID})
	defer op.End()

	input := &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		KeyConditionExpression: aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: "EARN!" + deID},
		},
		ScanIndexForward:  aws.Bool(false), // newest first
		Limit:             aws.Int32(pageSize),
		ExclusiveStartKey: lastKey,
	}

	if afterTimestamp != "" {
		input.KeyConditionExpression = aws.String("PK = :pk AND SK > :after")
		input.ExpressionAttributeValues[":after"] = &types.AttributeValueMemberS{Value: afterTimestamp}
	}

	result, err := r.client.Query(ctx, input)
	if err != nil {
		return nil, nil, op.Fail(fmt.Errorf("failed to query ledger: %w", err))
	}

	var entries []*models.EarningsLedger
	for _, item := range result.Items {
		var entry models.EarningsLedger
		if err := attributevalue.UnmarshalMap(item, &entry); err != nil {
			op.Logger().WithError(err).Warn("failed to unmarshal ledger entry; skipping")
			continue
		}
		entries = append(entries, &entry)
	}

	op.With("count", len(entries))
	return entries, result.LastEvaluatedKey, nil
}

// SumByDEAfter sums AmountZMW for all ledger entries for a DE after a given timestamp.
// Used to compute outstanding balance since last disbursement.
// Handles DynamoDB pagination internally.
func (r *EarningsLedgerRepository) SumByDEAfter(ctx context.Context, deID, afterTimestamp string) (float64, error) {
	op := logging.Start(ctx, r.logger, "EarningsLedger.SumByDEAfter", logrus.Fields{"de_id": deID})
	defer op.End()

	var total float64
	var lastKey map[string]types.AttributeValue

	for {
		entries, nextKey, err := r.QueryByDE(ctx, deID, afterTimestamp, 50, lastKey)
		if err != nil {
			return 0, op.Fail(err)
		}
		for _, e := range entries {
			total += e.AmountZMW
		}
		if nextKey == nil {
			break
		}
		lastKey = nextKey
	}

	op.With("total_zmw", total)
	return total, nil
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./internal/repository/...
```

Expected: no output

- [ ] **Step 3: Commit**

```bash
git add internal/repository/earnings_ledger_repository.go
git commit -m "feat: add EarningsLedgerRepository with append, paginated query, and sum"
```

---

## Task 3: WeeklySummary Repository

**Files:**
- Create: `internal/repository/weekly_summary_repository.go`

- [ ] **Step 1: Write the repository**

```go
// internal/repository/weekly_summary_repository.go
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

type WeeklySummaryRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewWeeklySummaryRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *WeeklySummaryRepository {
	return &WeeklySummaryRepository{client: client, tableName: tableName, logger: logger}
}

// Create writes a new weekly summary. Uses attribute_not_exists to ensure idempotency —
// the weekly cron can safely re-run without double-counting.
func (r *WeeklySummaryRepository) Create(ctx context.Context, summary *models.DEWeeklySummary) error {
	op := logging.Start(ctx, r.logger, "WeeklySummary.Create", logrus.Fields{
		"de_id": summary.DEID, "week": summary.WeekStartDate,
	})
	defer op.End()

	summary.CreatedAt = time.Now().UTC().Format(time.RFC3339)

	item, err := attributevalue.MarshalMap(summary)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal weekly summary: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: summary.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: summary.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(r.tableName),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(PK) AND attribute_not_exists(SK)"),
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return op.Outcome("already_exists", fmt.Errorf("weekly summary already exists for DE %s week %s", summary.DEID, summary.WeekStartDate))
		}
		return op.Fail(fmt.Errorf("failed to create weekly summary: %w", err))
	}
	return nil
}

// GetByWeek fetches a weekly summary for a specific DE and week start date.
func (r *WeeklySummaryRepository) GetByWeek(ctx context.Context, deID, weekStartDate string) (*models.DEWeeklySummary, error) {
	op := logging.Start(ctx, r.logger, "WeeklySummary.GetByWeek", logrus.Fields{
		"de_id": deID, "week": weekStartDate,
	})
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "WEEKLY!" + deID},
			"SK": &types.AttributeValueMemberS{Value: weekStartDate},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get weekly summary: %w", err))
	}
	if result.Item == nil {
		return nil, nil
	}

	var summary models.DEWeeklySummary
	if err := attributevalue.UnmarshalMap(result.Item, &summary); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal weekly summary: %w", err))
	}
	return &summary, nil
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./internal/repository/...
```

Expected: no output

- [ ] **Step 3: Commit**

```bash
git add internal/repository/weekly_summary_repository.go
git commit -m "feat: add WeeklySummaryRepository with idempotent create and get-by-week"
```

---

## Task 4: Disbursement Repository

**Files:**
- Create: `internal/repository/disbursement_repository.go`

- [ ] **Step 1: Write the repository**

```go
// internal/repository/disbursement_repository.go
package repository

import (
	"context"
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

type DisbursementRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewDisbursementRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *DisbursementRepository {
	return &DisbursementRepository{client: client, tableName: tableName, logger: logger}
}

// Create records a new offline disbursement.
func (r *DisbursementRepository) Create(ctx context.Context, d *models.Disbursement) error {
	op := logging.Start(ctx, r.logger, "Disbursement.Create", logrus.Fields{
		"de_id": d.DEID, "amount": d.AmountZMW,
	})
	defer op.End()

	if d.DisbursedAt == "" {
		d.DisbursedAt = time.Now().UTC().Format(time.RFC3339)
	}

	item, err := attributevalue.MarshalMap(d)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal disbursement: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: d.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: d.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to create disbursement: %w", err))
	}
	return nil
}

// ListByDE returns all disbursements for a DE sorted by disbursed_at descending.
func (r *DisbursementRepository) ListByDE(ctx context.Context, deID string) ([]*models.Disbursement, error) {
	op := logging.Start(ctx, r.logger, "Disbursement.ListByDE", logrus.Fields{"de_id": deID})
	defer op.End()

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		KeyConditionExpression: aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: "DISBURSEMENT!" + deID},
		},
		ScanIndexForward: aws.Bool(false), // newest first
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to list disbursements: %w", err))
	}

	var items []*models.Disbursement
	for _, item := range result.Items {
		var d models.Disbursement
		if err := attributevalue.UnmarshalMap(item, &d); err != nil {
			continue
		}
		items = append(items, &d)
	}

	op.With("count", len(items))
	return items, nil
}

// GetLatest returns the most recent disbursement for a DE, or nil if none exists.
// Used to determine last_disbursed_at for outstanding balance computation.
func (r *DisbursementRepository) GetLatest(ctx context.Context, deID string) (*models.Disbursement, error) {
	op := logging.Start(ctx, r.logger, "Disbursement.GetLatest", logrus.Fields{"de_id": deID})
	defer op.End()

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		KeyConditionExpression: aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: "DISBURSEMENT!" + deID},
		},
		ScanIndexForward: aws.Bool(false),
		Limit:            aws.Int32(1),
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get latest disbursement: %w", err))
	}
	if len(result.Items) == 0 {
		return nil, nil
	}

	var d models.Disbursement
	if err := attributevalue.UnmarshalMap(result.Items[0], &d); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal disbursement: %w", err))
	}
	return &d, nil
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./internal/repository/...
```

Expected: no output

- [ ] **Step 3: Commit**

```bash
git add internal/repository/disbursement_repository.go
git commit -m "feat: add DisbursementRepository with create, list, and get-latest"
```

---

## Phase B1 Complete

**What this phase delivers:**
- `EarningsLedger` model + repository — unified sorted earning events per DE
- `DEWeeklySummary` model + repository — weekly bonus records with idempotent create
- `Disbursement` model + repository — offline payout records with latest lookup

**Phase B2 picks up here** by writing ledger entries at trip completion via `PayoutService`.
