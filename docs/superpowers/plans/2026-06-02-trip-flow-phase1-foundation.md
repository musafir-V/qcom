# Trip Flow — Phase 1: Foundation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Define the Trip and Task data models, implement the Trip repository with all DynamoDB access patterns, create the CronLock repository, and provision the required GSIs. After this phase the Trip entity can be created, read, and queried — ready for the assignment cron in Phase 2.

**Architecture:** Trip is a DynamoDB single-table item (`PK=TRIP!{tripId}, SK=METADATA`) with Tasks embedded as a list attribute. Two GSIs — `OrderIndex` (look up trip by order_id) and `DETripsIndex` (query trips by DE for earnings) — plus a `CronLock` item for the distributed assignment lock. All timestamps use Africa/Lusaka (UTC+2).

**Tech Stack:** Go 1.24, AWS DynamoDB (aws-sdk-go-v2), logrus, uuid

**Prerequisites:** None. This phase has no dependencies on other plans.

---

## Cross-Cutting Rule: Timezone
Every date/time computation in this codebase uses **Africa/Lusaka (UTC+2)**. The `zambiaLoc` variable already exists in `internal/service/qr_service.go` as a package-level var. Phase 1 introduces a shared timezone helper so all packages can use it without importing the service package.

---

## File Map

### New Files
- `internal/timezone/timezone.go` — shared `ZambiaLocation()` and `ZambiaDate()` helpers
- `internal/models/trip.go` — Trip, Task, TripStatus, TaskType, TaskStatus models
- `internal/repository/trip_repository.go` — Trip CRUD + OrderIndex + DETripsIndex queries
- `internal/repository/cron_lock_repository.go` — distributed lock acquire/release

### Modified Files
- `internal/service/qr_service.go` — replace local `zambiaLoc` with `timezone.ZambiaLocation()`
- `cmd/server/main.go` — wire `TripRepository` and `CronLockRepository`

---

## Task 1: Shared Timezone Package

**Files:**
- Create: `internal/timezone/timezone.go`
- Modify: `internal/service/qr_service.go`

- [ ] **Step 1: Write the timezone package**

```go
// internal/timezone/timezone.go
package timezone

import "time"

var zambiaLoc = mustLoad()

func mustLoad() *time.Location {
	loc, err := time.LoadLocation("Africa/Lusaka")
	if err != nil {
		loc = time.FixedZone("CAT", 2*60*60)
	}
	return loc
}

// ZambiaLocation returns the Africa/Lusaka *time.Location (UTC+2).
// Use this for ALL business-logic date boundaries in this codebase.
func ZambiaLocation() *time.Location { return zambiaLoc }

// Now returns the current time in Zambia timezone.
func Now() time.Time { return time.Now().In(zambiaLoc) }

// DateString returns today's date as "2006-01-02" in Zambia timezone.
func DateString() string { return Now().Format("2006-01-02") }

// WeekStart returns the Monday of the current week in Zambia timezone,
// formatted as "2006-01-02".
func WeekStart() string {
	now := Now()
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7 // Sunday = 7 in ISO week
	}
	monday := now.AddDate(0, 0, -(weekday - 1))
	return monday.Format("2006-01-02")
}
```

- [ ] **Step 2: Update `qr_service.go` to use the shared package**

In `internal/service/qr_service.go`, remove:
```go
var zambiaLoc = mustLoadLocation("Africa/Lusaka")

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc = time.FixedZone("CAT", 2*60*60)
	}
	return loc
}
```

Replace every reference to `zambiaLoc` with `timezone.ZambiaLocation()` and add the import:
```go
import "github.com/qcom/qcom/internal/timezone"
```

The three references to replace are in `GenerateQRCode`, `ValidUntil`, and `ValidateQRCode`:
```go
// GenerateQRCode
now := time.Now().In(timezone.ZambiaLocation())

// ValidUntil
now := time.Now().In(timezone.ZambiaLocation())
return time.Date(now.Year(), now.Month(), now.Day(), now.Hour()+1, 0, 0, 0, timezone.ZambiaLocation())

// ValidateQRCode
now := time.Now().In(timezone.ZambiaLocation())
```

- [ ] **Step 3: Verify everything compiles and existing QR tests pass**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./... && go test ./internal/service/... -run TestQR -v
```

Expected: build succeeds, any existing QR tests pass

- [ ] **Step 4: Commit**

```bash
git add internal/timezone/timezone.go internal/service/qr_service.go
git commit -m "refactor: extract shared timezone package, use Africa/Lusaka everywhere"
```

---

## Task 2: Trip and Task Models

**Files:**
- Create: `internal/models/trip.go`

- [ ] **Step 1: Write the models**

```go
// internal/models/trip.go
package models

type TripStatus string
type TaskType string
type TaskStatus string

const (
	TripStatusCreated    TripStatus = "created"
	TripStatusAssigned   TripStatus = "assigned"
	TripStatusInTransit  TripStatus = "in_transit"
	TripStatusReached    TripStatus = "reached"
	TripStatusCompleted  TripStatus = "completed"
	TripStatusCancelled  TripStatus = "cancelled"

	TaskTypePickup TaskType = "pickup"
	TaskTypeDrop   TaskType = "drop"

	TaskStatusCreated   TaskStatus = "created"
	TaskStatusArrived   TaskStatus = "arrived"
	TaskStatusReached   TaskStatus = "reached"
	TaskStatusCompleted TaskStatus = "completed"
)

type Task struct {
	TaskID  string     `json:"task_id" dynamodbav:"task_id"`
	Type    TaskType   `json:"type" dynamodbav:"type"`
	Status  TaskStatus `json:"status" dynamodbav:"status"`
	Phone   string     `json:"phone" dynamodbav:"phone"`
	Address string     `json:"address" dynamodbav:"address"`
	Lat     float64    `json:"lat" dynamodbav:"lat"`
	Lng     float64    `json:"lng" dynamodbav:"lng"`
	OTP     string     `json:"otp,omitempty" dynamodbav:"otp,omitempty"` // drop task only
}

type Trip struct {
	TripID  string     `json:"trip_id" dynamodbav:"trip_id"`
	OrderID string     `json:"order_id" dynamodbav:"order_id"`
	StoreID string     `json:"store_id" dynamodbav:"store_id"`
	DEID    string     `json:"de_id,omitempty" dynamodbav:"de_id,omitempty"`
	Status  TripStatus `json:"status" dynamodbav:"status"`
	Tasks   []Task     `json:"tasks" dynamodbav:"tasks"`

	// Payout — set at creation (base) and completion (bonus+total)
	DistanceKM        float64 `json:"distance_km" dynamodbav:"distance_km"`
	BasePayZMW        float64 `json:"base_pay_zmw" dynamodbav:"base_pay_zmw"`
	BonusPayZMW       float64 `json:"bonus_pay_zmw" dynamodbav:"bonus_pay_zmw"`
	TotalPayZMW       float64 `json:"total_pay_zmw" dynamodbav:"total_pay_zmw"`
	DeliveryRankOfDay int     `json:"delivery_rank_of_day" dynamodbav:"delivery_rank_of_day"`

	CreatedAt   string `json:"created_at" dynamodbav:"created_at"`
	UpdatedAt   string `json:"updated_at" dynamodbav:"updated_at"`
	AssignedAt  string `json:"assigned_at,omitempty" dynamodbav:"assigned_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty" dynamodbav:"completed_at,omitempty"`
	CancelledAt string `json:"cancelled_at,omitempty" dynamodbav:"cancelled_at,omitempty"`
}

func (t *Trip) GetPK() string { return "TRIP!" + t.TripID }
func (t *Trip) GetSK() string { return "METADATA" }

// PickupTask returns the pickup task from the embedded list.
func (t *Trip) PickupTask() *Task {
	for i := range t.Tasks {
		if t.Tasks[i].Type == TaskTypePickup {
			return &t.Tasks[i]
		}
	}
	return nil
}

// DropTask returns the drop task from the embedded list.
func (t *Trip) DropTask() *Task {
	for i := range t.Tasks {
		if t.Tasks[i].Type == TaskTypeDrop {
			return &t.Tasks[i]
		}
	}
	return nil
}

// TaskByID returns the task with the given ID.
func (t *Trip) TaskByID(taskID string) *Task {
	for i := range t.Tasks {
		if t.Tasks[i].TaskID == taskID {
			return &t.Tasks[i]
		}
	}
	return nil
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./internal/models/...
```

Expected: no output

- [ ] **Step 3: Commit**

```bash
git add internal/models/trip.go
git commit -m "feat: add Trip and Task models with status constants and helper methods"
```

---

## Task 3: Trip Repository

**Files:**
- Create: `internal/repository/trip_repository.go`

- [ ] **Step 1: Write the repository**

```go
// internal/repository/trip_repository.go
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

type TripRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewTripRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *TripRepository {
	return &TripRepository{client: client, tableName: tableName, logger: logger}
}

// Create inserts a new Trip. Fails if a trip with the same PK already exists.
func (r *TripRepository) Create(ctx context.Context, trip *models.Trip) error {
	op := logging.Start(ctx, r.logger, "TripRepository.Create", logrus.Fields{
		"trip_id":  trip.TripID,
		"order_id": trip.OrderID,
	})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)
	trip.CreatedAt = now
	trip.UpdatedAt = now

	item, err := attributevalue.MarshalMap(trip)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal trip: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: trip.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: trip.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(r.tableName),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return op.Outcome("already_exists", fmt.Errorf("trip already exists for order %s", trip.OrderID))
		}
		return op.Fail(fmt.Errorf("failed to create trip: %w", err))
	}
	return nil
}

// GetByID fetches a trip by its trip_id (primary key lookup).
func (r *TripRepository) GetByID(ctx context.Context, tripID string) (*models.Trip, error) {
	op := logging.Start(ctx, r.logger, "TripRepository.GetByID", logrus.Fields{"trip_id": tripID})
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "TRIP!" + tripID},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get trip: %w", err))
	}
	if result.Item == nil {
		return nil, nil
	}

	var trip models.Trip
	if err := attributevalue.UnmarshalMap(result.Item, &trip); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal trip: %w", err))
	}
	return &trip, nil
}

// GetByOrderID fetches a trip using the OrderIndex GSI.
// Returns nil if no trip exists for this order yet.
func (r *TripRepository) GetByOrderID(ctx context.Context, orderID string) (*models.Trip, error) {
	op := logging.Start(ctx, r.logger, "TripRepository.GetByOrderID", logrus.Fields{"order_id": orderID})
	defer op.End()

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		IndexName:              aws.String("OrderIndex"),
		KeyConditionExpression: aws.String("order_id = :oid"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":oid": &types.AttributeValueMemberS{Value: orderID},
		},
		Limit: aws.Int32(1),
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to query OrderIndex: %w", err))
	}
	if len(result.Items) == 0 {
		return nil, nil
	}

	var trip models.Trip
	if err := attributevalue.UnmarshalMap(result.Items[0], &trip); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal trip: %w", err))
	}
	return &trip, nil
}

// Assign atomically sets de_id and status=assigned on the trip, and sets
// status=busy + current_order_id on the DE — in a single DynamoDB transaction.
// Conditions: trip must have no de_id yet; DE must have status=eligible.
func (r *TripRepository) Assign(ctx context.Context, tripID, orderID, deID, dePhone, assignedAt string) error {
	op := logging.Start(ctx, r.logger, "TripRepository.Assign", logrus.Fields{
		"trip_id": tripID, "de_id": deID,
	})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)

	_, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				Update: &types.Update{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: "TRIP!" + tripID},
						"SK": &types.AttributeValueMemberS{Value: "METADATA"},
					},
					UpdateExpression: aws.String("SET #status = :assigned, de_id = :de_id, assigned_at = :at, updated_at = :now"),
					ConditionExpression: aws.String("attribute_not_exists(de_id)"),
					ExpressionAttributeNames: map[string]string{"#status": "status"},
					ExpressionAttributeValues: map[string]types.AttributeValue{
						":assigned": &types.AttributeValueMemberS{Value: string(models.TripStatusAssigned)},
						":de_id":    &types.AttributeValueMemberS{Value: deID},
						":at":       &types.AttributeValueMemberS{Value: assignedAt},
						":now":      &types.AttributeValueMemberS{Value: now},
					},
				},
			},
			{
				Update: &types.Update{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: "DE!" + dePhone},
						"SK": &types.AttributeValueMemberS{Value: "METADATA"},
					},
					UpdateExpression: aws.String("SET #status = :busy, current_order_id = :oid, current_trip_id = :tid, updated_at = :now REMOVE duty_index_key"),
					ConditionExpression: aws.String("#status = :eligible"),
					ExpressionAttributeNames: map[string]string{"#status": "status"},
					ExpressionAttributeValues: map[string]types.AttributeValue{
						":busy":     &types.AttributeValueMemberS{Value: "busy"},
						":eligible": &types.AttributeValueMemberS{Value: "eligible"},
						":oid":      &types.AttributeValueMemberS{Value: orderID},
						":tid":      &types.AttributeValueMemberS{Value: tripID},
						":now":      &types.AttributeValueMemberS{Value: now},
					},
				},
			},
		},
	})
	if err != nil {
		var txErr *types.TransactionCanceledException
		if errors.As(err, &txErr) {
			return op.Outcome("conflict", fmt.Errorf("assignment conflict: trip already assigned or DE no longer eligible"))
		}
		return op.Fail(fmt.Errorf("failed to assign trip: %w", err))
	}
	return nil
}

// UpdateStatus updates the trip status and sets updated_at.
func (r *TripRepository) UpdateStatus(ctx context.Context, tripID string, status models.TripStatus) error {
	op := logging.Start(ctx, r.logger, "TripRepository.UpdateStatus", logrus.Fields{
		"trip_id": tripID, "status": string(status),
	})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)
	expr := "SET #status = :status, updated_at = :now"
	values := map[string]types.AttributeValue{
		":status": &types.AttributeValueMemberS{Value: string(status)},
		":now":    &types.AttributeValueMemberS{Value: now},
	}

	if status == models.TripStatusCompleted {
		expr += ", completed_at = :now"
	} else if status == models.TripStatusCancelled {
		expr += ", cancelled_at = :now"
	}

	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "TRIP!" + tripID},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression:          aws.String(expr),
		ExpressionAttributeNames:  map[string]string{"#status": "status"},
		ExpressionAttributeValues: values,
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to update trip status: %w", err))
	}
	return nil
}

// UpdateTasks replaces the entire tasks list on the trip item.
func (r *TripRepository) UpdateTasks(ctx context.Context, tripID string, tasks []models.Task) error {
	op := logging.Start(ctx, r.logger, "TripRepository.UpdateTasks", logrus.Fields{"trip_id": tripID})
	defer op.End()

	tasksAttr, err := attributevalue.Marshal(tasks)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal tasks: %w", err))
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "TRIP!" + tripID},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression: aws.String("SET tasks = :tasks, updated_at = :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":tasks": tasksAttr,
			":now":   &types.AttributeValueMemberS{Value: now},
		},
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to update tasks: %w", err))
	}
	return nil
}

// UpdatePayout stamps base_pay_zmw and distance_km at assignment time,
// and bonus_pay_zmw + total_pay_zmw + delivery_rank_of_day at completion time.
func (r *TripRepository) UpdatePayout(ctx context.Context, tripID string, distanceKM, basePayZMW, bonusPayZMW, totalPayZMW float64, rankOfDay int) error {
	op := logging.Start(ctx, r.logger, "TripRepository.UpdatePayout", logrus.Fields{"trip_id": tripID})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "TRIP!" + tripID},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression: aws.String("SET distance_km = :dist, base_pay_zmw = :base, bonus_pay_zmw = :bonus, total_pay_zmw = :total, delivery_rank_of_day = :rank, updated_at = :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":dist":  &types.AttributeValueMemberN{Value: fmt.Sprintf("%.4f", distanceKM)},
			":base":  &types.AttributeValueMemberN{Value: fmt.Sprintf("%.2f", basePayZMW)},
			":bonus": &types.AttributeValueMemberN{Value: fmt.Sprintf("%.2f", bonusPayZMW)},
			":total": &types.AttributeValueMemberN{Value: fmt.Sprintf("%.2f", totalPayZMW)},
			":rank":  &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", rankOfDay)},
			":now":   &types.AttributeValueMemberS{Value: now},
		},
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to update trip payout: %w", err))
	}
	return nil
}

// ListByDEAfter queries the DETripsIndex GSI for all trips completed by a DE
// after a given timestamp (used for earnings queries and outstanding balance).
// Pass "" for afterTimestamp to get all trips for this DE.
// Returns up to pageSize results; pass lastKey for pagination.
func (r *TripRepository) ListByDEAfter(ctx context.Context, deID, afterTimestamp string, pageSize int32, lastKey map[string]types.AttributeValue) ([]*models.Trip, map[string]types.AttributeValue, error) {
	op := logging.Start(ctx, r.logger, "TripRepository.ListByDEAfter", logrus.Fields{
		"de_id": deID, "after": afterTimestamp,
	})
	defer op.End()

	input := &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		IndexName:              aws.String("DETripsIndex"),
		KeyConditionExpression: aws.String("de_id = :de"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":de": &types.AttributeValueMemberS{Value: deID},
		},
		ScanIndexForward:  aws.Bool(false), // newest first
		Limit:             aws.Int32(pageSize),
		ExclusiveStartKey: lastKey,
	}

	if afterTimestamp != "" {
		input.KeyConditionExpression = aws.String("de_id = :de AND completed_at > :after")
		input.ExpressionAttributeValues[":after"] = &types.AttributeValueMemberS{Value: afterTimestamp}
	}

	result, err := r.client.Query(ctx, input)
	if err != nil {
		return nil, nil, op.Fail(fmt.Errorf("failed to query DETripsIndex: %w", err))
	}

	var trips []*models.Trip
	for _, item := range result.Items {
		var trip models.Trip
		if err := attributevalue.UnmarshalMap(item, &trip); err != nil {
			op.Logger().WithError(err).Warn("failed to unmarshal trip from DETripsIndex; skipping")
			continue
		}
		trips = append(trips, &trip)
	}

	op.With("count", len(trips))
	return trips, result.LastEvaluatedKey, nil
}

// CancelByOrderID cancels a trip and frees the assigned DE atomically.
// Used by the assignment cron when it detects a Java order has been cancelled.
// dePhone is needed to update the DE record; pass "" if the trip is unassigned.
func (r *TripRepository) CancelByOrderID(ctx context.Context, tripID, dePhone string) error {
	op := logging.Start(ctx, r.logger, "TripRepository.CancelByOrderID", logrus.Fields{"trip_id": tripID})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)

	items := []types.TransactWriteItem{
		{
			Update: &types.Update{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: "TRIP!" + tripID},
					"SK": &types.AttributeValueMemberS{Value: "METADATA"},
				},
				UpdateExpression: aws.String("SET #status = :cancelled, cancelled_at = :now, updated_at = :now"),
				ExpressionAttributeNames: map[string]string{"#status": "status"},
				ExpressionAttributeValues: map[string]types.AttributeValue{
					":cancelled": &types.AttributeValueMemberS{Value: string(models.TripStatusCancelled)},
					":now":       &types.AttributeValueMemberS{Value: now},
				},
			},
		},
	}

	if dePhone != "" {
		items = append(items, types.TransactWriteItem{
			Update: &types.Update{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: "DE!" + dePhone},
					"SK": &types.AttributeValueMemberS{Value: "METADATA"},
				},
				UpdateExpression: aws.String("SET #status = :free, updated_at = :now REMOVE current_order_id, current_trip_id, duty_index_key"),
				ExpressionAttributeNames: map[string]string{"#status": "status"},
				ExpressionAttributeValues: map[string]types.AttributeValue{
					":free": &types.AttributeValueMemberS{Value: "free"},
					":now":  &types.AttributeValueMemberS{Value: now},
				},
			},
		})
	}

	_, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: items,
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to cancel trip: %w", err))
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
git add internal/repository/trip_repository.go
git commit -m "feat: add TripRepository with CRUD, OrderIndex, DETripsIndex, Assign transaction, Cancel"
```

---

## Task 4: CronLock Repository

**Files:**
- Create: `internal/repository/cron_lock_repository.go`

- [ ] **Step 1: Write the repository**

```go
// internal/repository/cron_lock_repository.go
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

const cronLockPK = "CRON_LOCK"
const cronLockSK = "trip-assignment"

type CronLockRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewCronLockRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *CronLockRepository {
	return &CronLockRepository{client: client, tableName: tableName, logger: logger}
}

// Acquire attempts to acquire the distributed cron lock.
// Returns true if acquired, false if another instance holds it.
// The lock expires after ttlSeconds if Release is never called (e.g. instance crash).
func (r *CronLockRepository) Acquire(ctx context.Context, ttlSeconds int) (bool, error) {
	op := logging.Start(ctx, r.logger, "CronLock.Acquire", nil)
	defer op.End()

	expiresAt := time.Now().UTC().Add(time.Duration(ttlSeconds) * time.Second).Format(time.RFC3339)

	_, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item: map[string]types.AttributeValue{
			"PK":         &types.AttributeValueMemberS{Value: cronLockPK},
			"SK":         &types.AttributeValueMemberS{Value: cronLockSK},
			"expires_at": &types.AttributeValueMemberS{Value: expiresAt},
		},
		// Acquire if: lock doesn't exist OR it has expired
		ConditionExpression: aws.String("attribute_not_exists(PK) OR expires_at < :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":now": &types.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339)},
		},
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			op.With("acquired", false)
			return false, nil // another instance holds the lock
		}
		return false, op.Fail(fmt.Errorf("failed to acquire cron lock: %w", err))
	}

	op.With("acquired", true)
	return true, nil
}

// Release deletes the lock so the next tick can acquire it immediately.
// Always call this after a successful tick completes (even on partial failure).
func (r *CronLockRepository) Release(ctx context.Context) error {
	op := logging.Start(ctx, r.logger, "CronLock.Release", nil)
	defer op.End()

	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: cronLockPK},
			"SK": &types.AttributeValueMemberS{Value: cronLockSK},
		},
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to release cron lock: %w", err))
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
git add internal/repository/cron_lock_repository.go
git commit -m "feat: add CronLockRepository with acquire/release for distributed assignment cron"
```

---

## Task 5: Update DE Model + Repository

**Files:**
- Modify: `internal/models/delivery_executive.go`
- Modify: `internal/repository/de_repository.go`

- [ ] **Step 1: Add new fields to DE model**

In `internal/models/delivery_executive.go`, update the struct to add these fields after `CurrentOrderID`:

```go
CurrentTripID       string `json:"current_trip_id,omitempty" dynamodbav:"current_trip_id,omitempty"`
TotalTripsCompleted int    `json:"total_trips_completed" dynamodbav:"total_trips_completed"`
DailyTripCount      int    `json:"daily_trip_count" dynamodbav:"daily_trip_count"`
DailyCountDate      string `json:"daily_count_date,omitempty" dynamodbav:"daily_count_date,omitempty"`
LastDisbursedAt     string `json:"last_disbursed_at,omitempty" dynamodbav:"last_disbursed_at,omitempty"`
```

**Note:** `ReferralCode` is also added here if Plan C has not yet been executed. The full struct should be:

```go
type DeliveryExecutive struct {
	DEID                string   `json:"de_id" dynamodbav:"de_id"`
	PhoneNumber         string   `json:"phone_number" dynamodbav:"phone_number"`
	Name                string   `json:"name" dynamodbav:"name"`
	ProfileURL          string   `json:"profile_url" dynamodbav:"profile_url"`
	NRCURL              string   `json:"nrc_url" dynamodbav:"nrc_url"`
	Status              DEStatus `json:"status" dynamodbav:"status"`
	DutyIndexKey        string   `json:"duty_index_key,omitempty" dynamodbav:"duty_index_key,omitempty"`
	CurrentStoreID      string   `json:"current_store_id,omitempty" dynamodbav:"current_store_id,omitempty"`
	CurrentOrderID      string   `json:"current_order_id,omitempty" dynamodbav:"current_order_id,omitempty"`
	CurrentTripID       string   `json:"current_trip_id,omitempty" dynamodbav:"current_trip_id,omitempty"`
	TotalTripsCompleted int      `json:"total_trips_completed" dynamodbav:"total_trips_completed"`
	DailyTripCount      int      `json:"daily_trip_count" dynamodbav:"daily_trip_count"`
	DailyCountDate      string   `json:"daily_count_date,omitempty" dynamodbav:"daily_count_date,omitempty"`
	LastDisbursedAt     string   `json:"last_disbursed_at,omitempty" dynamodbav:"last_disbursed_at,omitempty"`
	ReferralCode        string   `json:"referral_code,omitempty" dynamodbav:"referral_code,omitempty"`
	CreatedAt           string   `json:"created_at" dynamodbav:"created_at"`
	UpdatedAt           string   `json:"updated_at" dynamodbav:"updated_at"`
}
```

- [ ] **Step 2: Add `FindEligibleByStoreFIFO` to DE repository**

Add this method to `internal/repository/de_repository.go`. It replaces the existing `FindEligibleByStore` scan with a GSI query that returns results sorted by `updated_at` ascending (FIFO — DE who went eligible first):

```go
// FindEligibleByStoreFIFO returns eligible DEs for a store sorted by updated_at ascending.
// Uses the DEDutyIndex GSI (PK: duty_index_key).
func (r *DERepository) FindEligibleByStoreFIFO(ctx context.Context, storeID string) ([]*models.DeliveryExecutive, error) {
	op := logging.Start(ctx, r.logger, "FindEligibleByStoreFIFO", logrus.Fields{"store_id": storeID})
	defer op.End()

	dutyKey := "DE_ELIGIBLE#" + storeID

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		IndexName:              aws.String("DEDutyIndex"),
		KeyConditionExpression: aws.String("duty_index_key = :duty_key"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":duty_key": &types.AttributeValueMemberS{Value: dutyKey},
		},
		ScanIndexForward: aws.Bool(true), // ascending updated_at = FIFO
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to find eligible DEs: %w", err))
	}

	var des []*models.DeliveryExecutive
	for _, item := range result.Items {
		var de models.DeliveryExecutive
		if err := attributevalue.UnmarshalMap(item, &de); err != nil {
			op.Logger().WithError(err).Warn("failed to unmarshal DE; skipping")
			continue
		}
		des = append(des, &de)
	}

	op.With("count", len(des))
	return des, nil
}
```

Also add `IncrementDailyCount` which handles the lazy daily reset:

```go
// IncrementDailyCount atomically increments the DE's daily trip count,
// resetting it to 1 if the stored date differs from today (Zambia timezone).
// Also increments TotalTripsCompleted unconditionally.
// Returns the new daily count after increment.
func (r *DERepository) IncrementDailyCount(ctx context.Context, phone, todayZambia string) (int, error) {
	op := logging.Start(ctx, r.logger, "IncrementDailyCount", logrus.Fields{"phone": phone})
	defer op.End()

	// First fetch current state
	de, err := r.GetByPhone(ctx, phone)
	if err != nil || de == nil {
		return 0, op.Fail(fmt.Errorf("failed to fetch DE for daily count: %w", err))
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var newCount int
	var expr string
	var values map[string]types.AttributeValue

	if de.DailyCountDate != todayZambia {
		// New day — reset to 1
		newCount = 1
		expr = "SET daily_trip_count = :one, daily_count_date = :today, total_trips_completed = total_trips_completed + :one, updated_at = :now"
		values = map[string]types.AttributeValue{
			":one":   &types.AttributeValueMemberN{Value: "1"},
			":today": &types.AttributeValueMemberS{Value: todayZambia},
			":now":   &types.AttributeValueMemberS{Value: now},
		}
	} else {
		// Same day — increment
		newCount = de.DailyTripCount + 1
		expr = "SET daily_trip_count = daily_trip_count + :one, total_trips_completed = total_trips_completed + :one, updated_at = :now"
		values = map[string]types.AttributeValue{
			":one": &types.AttributeValueMemberN{Value: "1"},
			":now": &types.AttributeValueMemberS{Value: now},
		}
	}

	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression:          aws.String(expr),
		ExpressionAttributeValues: values,
	})
	if err != nil {
		return 0, op.Fail(fmt.Errorf("failed to increment daily count: %w", err))
	}

	return newCount, nil
}
```

- [ ] **Step 3: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./...
```

Expected: no output

- [ ] **Step 4: Commit**

```bash
git add internal/models/delivery_executive.go internal/repository/de_repository.go
git commit -m "feat: add CurrentTripID, DailyTripCount, TotalTripsCompleted to DE model; add FindEligibleByStoreFIFO and IncrementDailyCount"
```

---

## Task 6: DynamoDB GSIs

Two GSIs must exist before any trip code can run. This is an infrastructure task.

- [ ] **Step 1: Add `OrderIndex` GSI**

```
Index name:    OrderIndex
Partition key: order_id (String)
Sort key:      (none)
Projection:    ALL
```

```bash
aws dynamodb update-table \
  --table-name QComTable \
  --attribute-definitions AttributeName=order_id,AttributeType=S \
  --global-secondary-index-updates \
    '[{"Create":{"IndexName":"OrderIndex","KeySchema":[{"AttributeName":"order_id","KeyType":"HASH"}],"Projection":{"ProjectionType":"ALL"}}}]' \
  --endpoint-url http://localhost:8000 \
  --region us-east-1
```

- [ ] **Step 2: Add `DETripsIndex` GSI**

```
Index name:    DETripsIndex
Partition key: de_id (String)
Sort key:      completed_at (String)
Projection:    ALL
```

```bash
aws dynamodb update-table \
  --table-name QComTable \
  --attribute-definitions AttributeName=de_id,AttributeType=S AttributeName=completed_at,AttributeType=S \
  --global-secondary-index-updates \
    '[{"Create":{"IndexName":"DETripsIndex","KeySchema":[{"AttributeName":"de_id","KeyType":"HASH"},{"AttributeName":"completed_at","KeyType":"RANGE"}],"Projection":{"ProjectionType":"ALL"}}}]' \
  --endpoint-url http://localhost:8000 \
  --region us-east-1
```

- [ ] **Step 3: Verify both GSIs are ACTIVE**

```bash
aws dynamodb describe-table --table-name QComTable \
  --endpoint-url http://localhost:8000 --region us-east-1 \
  | grep -A3 '"IndexName"'
```

Expected output includes:
```
"IndexName": "OrderIndex",
...
"IndexName": "DETripsIndex",
```

Both with `"IndexStatus": "ACTIVE"`

- [ ] **Step 4: Note — DEDutyIndex must also have `updated_at` as sort key for FIFO**

The existing `DEDutyIndex` (PK: `duty_index_key`) needs `updated_at` as its sort key to enable FIFO ordering. If it was created without a sort key, recreate it:

```
Index name:    DEDutyIndex
Partition key: duty_index_key (String)
Sort key:      updated_at (String)
Projection:    ALL
```

- [ ] **Step 5: Commit infra note**

```bash
git commit --allow-empty -m "infra: OrderIndex, DETripsIndex, DEDutyIndex(with updated_at SK) GSIs provisioned"
```

---

## Task 7: Wire Up in main.go

**Files:**
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Add new repos to `main.go`**

In the repository initialization section, add:

```go
tripRepo := repository.NewTripRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
cronLockRepo := repository.NewCronLockRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
```

- [ ] **Step 2: Build the full server**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./...
```

Expected: no output

- [ ] **Step 3: Commit**

```bash
git add cmd/server/main.go
git commit -m "feat: wire TripRepository and CronLockRepository into server"
```

---

## Phase 1 Complete

**What this phase delivers:**
- `Trip` and `Task` models with full status constants and helper methods
- `TripRepository` with all access patterns: create, get by ID, get by order (OrderIndex), assign (transaction), update status, update tasks, update payout, list by DE (DETripsIndex), cancel (transaction)
- `CronLockRepository` with acquire/release
- `DERepository` extended with `FindEligibleByStoreFIFO` and `IncrementDailyCount`
- Shared `timezone` package — Africa/Lusaka used everywhere
- Two new GSIs provisioned: `OrderIndex`, `DETripsIndex`

**Phase 2 picks up here** by importing `TripRepository` and `CronLockRepository` to build the assignment cron.
