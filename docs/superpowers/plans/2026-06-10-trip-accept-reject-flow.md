# Trip Accept/Reject Flow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give a driver an accept/reject window on an assigned trip, auto-reject (back to the pool) after a configurable timeout, and enforce a strict trip state machine so tasks can't be progressed out of order.

**Architecture:** A trip flows `created → assigned → accepted → out_for_delivery → completed` (plus `cancelled`, plus a reject branch back to `created`). Accept/reject are trip-level endpoints. Auto-reject is **not** a timer — an `accept_deadline` is stamped at assignment and the existing 10s assignment cron sweeps expired `assigned` trips back into the pool, re-assigning them the same tick while skipping any DE that already rejected the trip. Java is never told about accept/reject; the customer track API hides the whole accept window (shows `finding_driver` until a driver accepts). An admin escape-hatch endpoint force-assigns a stuck (`created`) order directly to `accepted`.

**Tech Stack:** Go, gorilla/mux, AWS SDK v2 DynamoDB (single-table, `PK`/`SK`), logrus. Tests: standard `testing` (pure unit tests for validators; `//go:build integration` for end-to-end).

---

## Design decisions locked during brainstorming

- **Rename only** `in_transit → out_for_delivery`. `completed` stays `completed`; `completed_at`/`cancelled_at` field names unchanged. Delete the dead `reached` status. Add `accepted`. **Greenfield — no data migration.**
- **Task statuses are unchanged** (`created → completed`, two values). `accepted` is a *trip* status only. Accept/reject act on the trip, never on a task.
- **Reject returns the DE to `eligible`** (rebuild `duty_index_key`), not `free`, so they keep their shift and can get the next order.
- **Reject is only legal from `assigned`.** `accepted → created` is illegal (no reject after accept).
- **Fairness:** trip carries `rejected_de_ids`; cron skips those DEs. If everyone eligible has rejected, the trip parks in `created` (no cooldown) until a new DE appears or admin assigns.
- **Admin assign** input is `{ order_id, driver_phone }`. NOTE: the codebase looks DEs up by **phone** (PK `DE!<phone>`) and has no `de_id` index, so the admin endpoint identifies the driver by phone number. Preconditions: trip is `created`, DE is `eligible`. Lands the trip **directly in `accepted`** (no window). Auth is assumed handled upstream — this plan adds no auth gate.
- **Config:** new `AssignmentConfig` row (`PK=CONFIG / SK=ASSIGNMENT_V1`), key `auto_reject_time_seconds` (int), default `60`.
- **Java** is not notified about accept/reject/auto-reject. **Customer track API** reports `finding_driver` for both `created` and `assigned`; the driver is revealed only at `accepted`.

---

## File Structure & Conflict Map

Tasks run **sequentially** (a task commits before the next starts), so there are no concurrent edits. Files are grouped so each task owns a coherent slice and shared files are touched in as few tasks as possible.

| Task | New files | Modified files |
|---|---|---|
| 1. Trip model + transition validator | — | `internal/models/trip.go`, `internal/models/trip_test.go`, `internal/service/trip_service.go` (1-line rename only) |
| 2. AssignmentConfig model + repo | `internal/models/assignment_config.go`, `internal/models/assignment_config_test.go`, `internal/repository/assignment_config_repository.go` | — |
| 3. Trip repository methods | — | `internal/repository/trip_repository.go` |
| 4. Trip service: accept/reject + task gating | — | `internal/service/trip_service.go`, `internal/service/trip_service_test.go` |
| 5. Assignment cron: deadline, sweep, skip-rejected | — | `internal/service/assignment_cron.go`, `internal/service/assignment_cron_test.go`, `cmd/server/main.go` (cron DI) |
| 6. Driver accept/reject handlers + routes | — | `internal/handlers/trip_handlers.go`, `internal/handlers/trip_handlers_test.go`, `cmd/server/main.go` (routes) |
| 7. Admin assign service + handler + route | `internal/service/admin_service.go`, `internal/handlers/admin_handlers.go`, `internal/handlers/admin_handlers_test.go` | `cmd/server/main.go` (DI + route) |
| 8. Customer track API: hide accept window | — | `internal/handlers/track_handlers.go` |

`cmd/server/main.go` is the only file touched by more than two tasks (5, 6, 7); each touches a distinct region (cron constructor args / route registration / admin DI). Because tasks are sequential, these never conflict.

---

## Task 1: Trip model — statuses, fields, transition validator

**Files:**
- Modify: `internal/models/trip.go`
- Modify: `internal/service/trip_service.go:163` (mechanical rename to keep the build green)
- Test: `internal/models/trip_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/models/trip_test.go`:

```go
func TestIsValidTripTransition(t *testing.T) {
	valid := []struct{ from, to TripStatus }{
		{TripStatusCreated, TripStatusAssigned},
		{TripStatusCreated, TripStatusAccepted},        // admin hard-assign
		{TripStatusAssigned, TripStatusAccepted},       // driver accept
		{TripStatusAssigned, TripStatusCreated},        // reject / auto-reject
		{TripStatusAccepted, TripStatusOutForDelivery}, // pickup done
		{TripStatusOutForDelivery, TripStatusCompleted},// drop done
		{TripStatusCreated, TripStatusCancelled},
		{TripStatusAssigned, TripStatusCancelled},
		{TripStatusAccepted, TripStatusCancelled},
		{TripStatusOutForDelivery, TripStatusCancelled},
	}
	for _, c := range valid {
		if !IsValidTripTransition(c.from, c.to) {
			t.Errorf("expected %s -> %s to be valid", c.from, c.to)
		}
	}

	invalid := []struct{ from, to TripStatus }{
		{TripStatusAccepted, TripStatusCreated},          // no reject after accept
		{TripStatusAssigned, TripStatusOutForDelivery},   // must accept first
		{TripStatusCreated, TripStatusOutForDelivery},
		{TripStatusCompleted, TripStatusCreated},
		{TripStatusCancelled, TripStatusAssigned},
		{TripStatusOutForDelivery, TripStatusAccepted},
	}
	for _, c := range invalid {
		if IsValidTripTransition(c.from, c.to) {
			t.Errorf("expected %s -> %s to be INVALID", c.from, c.to)
		}
	}
}

func TestHasRejected(t *testing.T) {
	trip := &Trip{RejectedDEIDs: []string{"de-1", "de-2"}}
	if !trip.HasRejected("de-1") {
		t.Error("expected de-1 to be rejected")
	}
	if trip.HasRejected("de-3") {
		t.Error("expected de-3 to NOT be rejected")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/models/ -run 'TestIsValidTripTransition|TestHasRejected' -v`
Expected: FAIL — `undefined: TripStatusAccepted`, `undefined: TripStatusOutForDelivery`, `undefined: IsValidTripTransition`, `HasRejected`.

- [ ] **Step 3: Update the status constants and Trip fields**

In `internal/models/trip.go`, replace the `TripStatus` const block (lines 8–13) with:

```go
	TripStatusCreated        TripStatus = "created"
	TripStatusAssigned       TripStatus = "assigned"
	TripStatusAccepted       TripStatus = "accepted"
	TripStatusOutForDelivery TripStatus = "out_for_delivery"
	TripStatusCompleted      TripStatus = "completed"
	TripStatusCancelled      TripStatus = "cancelled"
```

(`TripStatusInTransit` and `TripStatusReached` are removed.)

In the `Trip` struct, add these fields after `DEID` (so the assigned driver's phone is available to the cron for auto-reject) and after `Tasks`:

```go
	DEID    string     `json:"de_id,omitempty" dynamodbav:"de_id,omitempty"`
	DEPhone string     `json:"de_phone,omitempty" dynamodbav:"de_phone,omitempty"`
```

and add, alongside the timestamp fields:

```go
	AcceptDeadline string   `json:"accept_deadline,omitempty" dynamodbav:"accept_deadline,omitempty"`
	RejectedDEIDs  []string `json:"rejected_de_ids,omitempty" dynamodbav:"rejected_de_ids,omitempty"`
```

- [ ] **Step 4: Add the transition validator and helper**

Append to `internal/models/trip.go`:

```go
// IsValidTripTransition reports whether a trip may move from `from` to `to`.
// This is the single source of truth for the trip state machine; any
// transition not listed here is illegal.
func IsValidTripTransition(from, to TripStatus) bool {
	switch from {
	case TripStatusCreated:
		return to == TripStatusAssigned || to == TripStatusAccepted || to == TripStatusCancelled
	case TripStatusAssigned:
		return to == TripStatusAccepted || to == TripStatusCreated || to == TripStatusCancelled
	case TripStatusAccepted:
		return to == TripStatusOutForDelivery || to == TripStatusCancelled
	case TripStatusOutForDelivery:
		return to == TripStatusCompleted || to == TripStatusCancelled
	default:
		return false
	}
}

// HasRejected reports whether the given DE has already rejected this trip.
func (t *Trip) HasRejected(deID string) bool {
	for _, id := range t.RejectedDEIDs {
		if id == deID {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Fix the one rename reference so the build compiles**

In `internal/service/trip_service.go:163`, change `models.TripStatusInTransit` to `models.TripStatusOutForDelivery`:

```go
		if err := s.tripRepo.UpdateStatus(bgCtx, trip.TripID, models.TripStatusOutForDelivery); err != nil {
```

- [ ] **Step 6: Run tests + build to verify pass**

Run: `go build ./... && go test ./internal/models/ -run 'TestIsValidTripTransition|TestHasRejected' -v`
Expected: build OK, both tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/models/trip.go internal/models/trip_test.go internal/service/trip_service.go
git commit -m "feat(trip): add accepted status, rename in_transit->out_for_delivery, add transition validator"
```

---

## Task 2: AssignmentConfig model + repository

**Files:**
- Create: `internal/models/assignment_config.go`
- Create: `internal/repository/assignment_config_repository.go`
- Test: `internal/models/assignment_config_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/models/assignment_config_test.go`:

```go
package models

import "testing"

func TestEffectiveAutoRejectSeconds_Default(t *testing.T) {
	c := &AssignmentConfig{}
	if got := c.EffectiveAutoRejectSeconds(); got != DefaultAutoRejectTimeSeconds {
		t.Fatalf("expected default %d, got %d", DefaultAutoRejectTimeSeconds, got)
	}
}

func TestEffectiveAutoRejectSeconds_Configured(t *testing.T) {
	c := &AssignmentConfig{AutoRejectTimeSeconds: 30}
	if got := c.EffectiveAutoRejectSeconds(); got != 30 {
		t.Fatalf("expected 30, got %d", got)
	}
}

func TestEffectiveAutoRejectSeconds_NonPositiveFallsBackToDefault(t *testing.T) {
	c := &AssignmentConfig{AutoRejectTimeSeconds: 0}
	if got := c.EffectiveAutoRejectSeconds(); got != DefaultAutoRejectTimeSeconds {
		t.Fatalf("expected default for 0, got %d", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/models/ -run TestEffectiveAutoRejectSeconds -v`
Expected: FAIL — `undefined: AssignmentConfig`.

- [ ] **Step 3: Create the model**

Create `internal/models/assignment_config.go`:

```go
package models

// DefaultAutoRejectTimeSeconds is the fallback accept window (1 minute) used
// when no AssignmentConfig row exists or the stored value is non-positive.
const DefaultAutoRejectTimeSeconds = 60

// AssignmentConfig holds operational knobs for trip assignment, stored as a
// singleton DynamoDB item (PK=CONFIG, SK=ASSIGNMENT_V1) so ops can tune it
// live without a redeploy.
type AssignmentConfig struct {
	AutoRejectTimeSeconds int `json:"auto_reject_time_seconds" dynamodbav:"auto_reject_time_seconds"`
}

func (c *AssignmentConfig) GetPK() string { return "CONFIG" }
func (c *AssignmentConfig) GetSK() string { return "ASSIGNMENT_V1" }

// EffectiveAutoRejectSeconds returns the configured window, or the default when
// unset/non-positive.
func (c *AssignmentConfig) EffectiveAutoRejectSeconds() int {
	if c.AutoRejectTimeSeconds <= 0 {
		return DefaultAutoRejectTimeSeconds
	}
	return c.AutoRejectTimeSeconds
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/models/ -run TestEffectiveAutoRejectSeconds -v`
Expected: PASS.

- [ ] **Step 5: Create the repository**

Create `internal/repository/assignment_config_repository.go` (mirrors `PayoutConfigRepository`, but returns a default-filled config when the item is missing so the cron never breaks):

```go
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

type AssignmentConfigRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewAssignmentConfigRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *AssignmentConfigRepository {
	return &AssignmentConfigRepository{client: client, tableName: tableName, logger: logger}
}

// Get returns the assignment config. When the item does not exist it returns a
// zero-value config (callers use EffectiveAutoRejectSeconds for the default),
// so a missing row is not an error.
func (r *AssignmentConfigRepository) Get(ctx context.Context) (*models.AssignmentConfig, error) {
	op := logging.Start(ctx, r.logger, "AssignmentConfigRepository.Get", nil)
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "CONFIG"},
			"SK": &types.AttributeValueMemberS{Value: "ASSIGNMENT_V1"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get assignment config: %w", err))
	}
	if result.Item == nil {
		return &models.AssignmentConfig{}, nil
	}

	var cfg models.AssignmentConfig
	if err := attributevalue.UnmarshalMap(result.Item, &cfg); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal assignment config: %w", err))
	}
	return &cfg, nil
}
```

- [ ] **Step 6: Build to verify it compiles**

Run: `go build ./...`
Expected: build OK.

- [ ] **Step 7: Commit**

```bash
git add internal/models/assignment_config.go internal/models/assignment_config_test.go internal/repository/assignment_config_repository.go
git commit -m "feat(config): add AssignmentConfig (auto_reject_time_seconds, default 60) + repository"
```

---

## Task 3: Trip repository — accept, reject-to-pool, admin-assign, deadline on Assign

**Files:**
- Modify: `internal/repository/trip_repository.go`

All four changes are in one file. No unit test here — DynamoDB repository methods are covered by the existing `//go:build integration` suite; correctness of the conditional expressions is verified end-to-end in Task 5/6 manual runs.

- [ ] **Step 1: Add `acceptDeadline` to `Assign` and stamp `de_phone` on the trip**

In `internal/repository/trip_repository.go`, change the `Assign` signature and the trip-side update. Replace the signature line:

```go
func (r *TripRepository) Assign(ctx context.Context, tripID, orderID, deID, dePhone, assignedAt string) error {
```

with:

```go
func (r *TripRepository) Assign(ctx context.Context, tripID, orderID, deID, dePhone, assignedAt, acceptDeadline string) error {
```

In the **trip** `Update` within that transaction, change the `UpdateExpression` and add the two new values:

```go
						UpdateExpression:         aws.String("SET #status = :assigned, de_id = :de_id, de_phone = :de_phone, assigned_at = :at, accept_deadline = :deadline, updated_at = :now"),
						ConditionExpression:      aws.String("attribute_not_exists(de_id)"),
						ExpressionAttributeNames: map[string]string{"#status": "status"},
						ExpressionAttributeValues: map[string]types.AttributeValue{
							":assigned": &types.AttributeValueMemberS{Value: string(models.TripStatusAssigned)},
							":de_id":    &types.AttributeValueMemberS{Value: deID},
							":de_phone": &types.AttributeValueMemberS{Value: dePhone},
							":at":       &types.AttributeValueMemberS{Value: assignedAt},
							":deadline": &types.AttributeValueMemberS{Value: acceptDeadline},
							":now":      &types.AttributeValueMemberS{Value: now},
						},
```

- [ ] **Step 2: Add `Accept`**

Append to `internal/repository/trip_repository.go`:

```go
// Accept transitions a trip from assigned to accepted for the owning DE.
// Conditional on the trip still being assigned to this DE — if the cron
// auto-rejected first, the condition fails and the caller gets a conflict.
func (r *TripRepository) Accept(ctx context.Context, tripID, deID string) error {
	op := logging.Start(ctx, r.logger, "TripRepository.Accept", logrus.Fields{"trip_id": tripID, "de_id": deID})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "TRIP!" + tripID},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression:         aws.String("SET #status = :accepted, updated_at = :now REMOVE accept_deadline"),
		ConditionExpression:      aws.String("#status = :assigned AND de_id = :de_id"),
		ExpressionAttributeNames: map[string]string{"#status": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":accepted": &types.AttributeValueMemberS{Value: string(models.TripStatusAccepted)},
			":assigned": &types.AttributeValueMemberS{Value: string(models.TripStatusAssigned)},
			":de_id":    &types.AttributeValueMemberS{Value: deID},
			":now":      &types.AttributeValueMemberS{Value: now},
		},
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return op.Outcome("conflict", fmt.Errorf("trip no longer assigned to this DE"))
		}
		return op.Fail(fmt.Errorf("failed to accept trip: %w", err))
	}
	return nil
}
```

- [ ] **Step 3: Add `RejectToPool`**

Append to `internal/repository/trip_repository.go`:

```go
// RejectToPool reverts an assigned trip back to the pool and returns the DE to
// eligible (NOT free) so they keep their shift and can take the next order.
// The DE is appended to the trip's rejected_de_ids so the cron won't re-offer
// the same trip to them. Conditional on the trip still being assigned.
func (r *TripRepository) RejectToPool(ctx context.Context, tripID, dePhone, storeID, deID string) error {
	op := logging.Start(ctx, r.logger, "TripRepository.RejectToPool", logrus.Fields{
		"trip_id": tripID, "de_id": deID,
	})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)
	dutyKey := "DE_ELIGIBLE#" + storeID

	_, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				Update: &types.Update{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: "TRIP!" + tripID},
						"SK": &types.AttributeValueMemberS{Value: "METADATA"},
					},
					UpdateExpression: aws.String(
						"SET #status = :created, rejected_de_ids = list_append(if_not_exists(rejected_de_ids, :empty), :newde), updated_at = :now " +
							"REMOVE de_id, de_phone, assigned_at, accept_deadline",
					),
					ConditionExpression:      aws.String("#status = :assigned"),
					ExpressionAttributeNames: map[string]string{"#status": "status"},
					ExpressionAttributeValues: map[string]types.AttributeValue{
						":created":  &types.AttributeValueMemberS{Value: string(models.TripStatusCreated)},
						":assigned": &types.AttributeValueMemberS{Value: string(models.TripStatusAssigned)},
						":empty":    &types.AttributeValueMemberL{Value: []types.AttributeValue{}},
						":newde":    &types.AttributeValueMemberL{Value: []types.AttributeValue{&types.AttributeValueMemberS{Value: deID}}},
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
					UpdateExpression: aws.String(
						"SET #status = :eligible, current_store_id = :store, duty_index_key = :duty, updated_at = :now " +
							"REMOVE current_order_id, current_trip_id",
					),
					ConditionExpression:      aws.String("#status = :busy AND current_trip_id = :tid"),
					ExpressionAttributeNames: map[string]string{"#status": "status"},
					ExpressionAttributeValues: map[string]types.AttributeValue{
						":eligible": &types.AttributeValueMemberS{Value: string(models.DEStatusEligible)},
						":busy":     &types.AttributeValueMemberS{Value: string(models.DEStatusBusy)},
						":store":    &types.AttributeValueMemberS{Value: storeID},
						":duty":     &types.AttributeValueMemberS{Value: dutyKey},
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
			return op.Outcome("conflict", fmt.Errorf("reject conflict: trip no longer assigned or DE not busy on this trip"))
		}
		return op.Fail(fmt.Errorf("failed to reject trip to pool: %w", err))
	}
	return nil
}
```

- [ ] **Step 4: Add `AdminAssign`**

Append to `internal/repository/trip_repository.go`:

```go
// AdminAssign force-assigns a created (pooled) trip directly to accepted for a
// given eligible DE, bypassing rejected_de_ids and the accept window. Used by
// the admin escape hatch. Conditional: trip must be created with no DE; DE must
// be eligible.
func (r *TripRepository) AdminAssign(ctx context.Context, tripID, orderID, deID, dePhone, storeID string) error {
	op := logging.Start(ctx, r.logger, "TripRepository.AdminAssign", logrus.Fields{
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
					UpdateExpression:         aws.String("SET #status = :accepted, de_id = :de_id, de_phone = :de_phone, assigned_at = :now, updated_at = :now"),
					ConditionExpression:      aws.String("#status = :created AND attribute_not_exists(de_id)"),
					ExpressionAttributeNames: map[string]string{"#status": "status"},
					ExpressionAttributeValues: map[string]types.AttributeValue{
						":accepted": &types.AttributeValueMemberS{Value: string(models.TripStatusAccepted)},
						":created":  &types.AttributeValueMemberS{Value: string(models.TripStatusCreated)},
						":de_id":    &types.AttributeValueMemberS{Value: deID},
						":de_phone": &types.AttributeValueMemberS{Value: dePhone},
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
					UpdateExpression:         aws.String("SET #status = :busy, current_order_id = :oid, current_trip_id = :tid, current_store_id = :store, updated_at = :now REMOVE duty_index_key"),
					ConditionExpression:      aws.String("#status = :eligible"),
					ExpressionAttributeNames: map[string]string{"#status": "status"},
					ExpressionAttributeValues: map[string]types.AttributeValue{
						":busy":     &types.AttributeValueMemberS{Value: string(models.DEStatusBusy)},
						":eligible": &types.AttributeValueMemberS{Value: string(models.DEStatusEligible)},
						":oid":      &types.AttributeValueMemberS{Value: orderID},
						":tid":      &types.AttributeValueMemberS{Value: tripID},
						":store":    &types.AttributeValueMemberS{Value: storeID},
						":now":      &types.AttributeValueMemberS{Value: now},
					},
				},
			},
		},
	})
	if err != nil {
		var txErr *types.TransactionCanceledException
		if errors.As(err, &txErr) {
			return op.Outcome("conflict", fmt.Errorf("admin assign conflict: trip not created or DE not eligible"))
		}
		return op.Fail(fmt.Errorf("failed to admin-assign trip: %w", err))
	}
	return nil
}
```

- [ ] **Step 5: Build to verify it compiles (Assign callers will break — fixed in Task 5)**

Run: `go vet ./internal/repository/`
Expected: the repository package compiles. NOTE: `go build ./...` will fail because `assignment_cron.go` still calls the old `Assign` signature — that caller is updated in Task 5. This is expected; proceed.

- [ ] **Step 6: Commit**

```bash
git add internal/repository/trip_repository.go
git commit -m "feat(trip-repo): add Accept, RejectToPool, AdminAssign; stamp accept_deadline + de_phone on Assign"
```

---

## Task 4: Trip service — accept/reject + task-completion gating

**Files:**
- Modify: `internal/service/trip_service.go`
- Test: `internal/service/trip_service_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/service/trip_service_test.go`:

```go
func TestValidateTaskAgainstTripStatus(t *testing.T) {
	cases := []struct {
		name     string
		taskType models.TaskType
		status   models.TripStatus
		wantErr  bool
	}{
		{"pickup allowed when accepted", models.TaskTypePickup, models.TripStatusAccepted, false},
		{"pickup blocked when assigned", models.TaskTypePickup, models.TripStatusAssigned, true},
		{"pickup blocked when created", models.TaskTypePickup, models.TripStatusCreated, true},
		{"drop allowed when out_for_delivery", models.TaskTypeDrop, models.TripStatusOutForDelivery, false},
		{"drop blocked when accepted (pickup not done)", models.TaskTypeDrop, models.TripStatusAccepted, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateTaskAgainstTripStatus(c.taskType, c.status)
			if c.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("expected nil, got %v", err)
			}
			if c.wantErr && err != nil && !errors.Is(err, ErrPrerequisiteIncomplete) {
				t.Fatalf("expected ErrPrerequisiteIncomplete, got %v", err)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/service/ -run TestValidateTaskAgainstTripStatus -v`
Expected: FAIL — `undefined: validateTaskAgainstTripStatus`.

- [ ] **Step 3: Add the gating validator and a new sentinel**

In `internal/service/trip_service.go`, add to the sentinel `var (...)` block:

```go
	ErrInvalidTripTransition = errors.New("invalid trip transition")
```

Append the validator function:

```go
// validateTaskAgainstTripStatus enforces that a task may only be completed when
// the trip is in the correct status: pickup requires the trip to be accepted;
// drop requires the trip to be out_for_delivery (i.e. pickup already done).
func validateTaskAgainstTripStatus(taskType models.TaskType, status models.TripStatus) error {
	switch taskType {
	case models.TaskTypePickup:
		if status != models.TripStatusAccepted {
			return fmt.Errorf("%w: accept the trip before starting pickup (status=%s)", ErrPrerequisiteIncomplete, status)
		}
	case models.TaskTypeDrop:
		if status != models.TripStatusOutForDelivery {
			return fmt.Errorf("%w: complete pickup before drop (status=%s)", ErrPrerequisiteIncomplete, status)
		}
	}
	return nil
}
```

- [ ] **Step 4: Wire the gate into `UpdateTaskStatus`**

In `internal/service/trip_service.go`, inside `UpdateTaskStatus`, immediately after the existing task-transition validation (after the `validateTaskTransition` block, before the drop-OTP check), insert:

```go
	// Trip-status gate: pickup requires accepted, drop requires out_for_delivery.
	if err := validateTaskAgainstTripStatus(task.Type, trip.Status); err != nil {
		return op.Outcome("prerequisite_incomplete", err)
	}
```

- [ ] **Step 5: Add `AcceptTrip` and `RejectTrip` service methods**

Append to `internal/service/trip_service.go`:

```go
// AcceptTrip moves an assigned trip to accepted for the calling DE.
func (s *TripService) AcceptTrip(ctx context.Context, tripID, callerDEPhone string) error {
	op := logging.Start(ctx, s.logger, "TripService.AcceptTrip", logrus.Fields{"trip_id": tripID})
	defer op.End()

	trip, de, err := s.ownedTrip(ctx, op, tripID, callerDEPhone)
	if err != nil {
		return err
	}
	if trip.Status != models.TripStatusAssigned {
		return op.Outcome("invalid_state", fmt.Errorf("%w: cannot accept from %s", ErrInvalidTripTransition, trip.Status))
	}
	if err := s.tripRepo.Accept(ctx, tripID, de.DEID); err != nil {
		return op.Fail(err)
	}
	return nil
}

// RejectTrip returns an assigned trip to the pool and the DE to eligible.
// Only legal while the trip is still assigned — once accepted it cannot be rejected.
func (s *TripService) RejectTrip(ctx context.Context, tripID, callerDEPhone string) error {
	op := logging.Start(ctx, s.logger, "TripService.RejectTrip", logrus.Fields{"trip_id": tripID})
	defer op.End()

	trip, de, err := s.ownedTrip(ctx, op, tripID, callerDEPhone)
	if err != nil {
		return err
	}
	if trip.Status != models.TripStatusAssigned {
		return op.Outcome("invalid_state", fmt.Errorf("%w: cannot reject from %s", ErrInvalidTripTransition, trip.Status))
	}
	if err := s.tripRepo.RejectToPool(ctx, tripID, de.PhoneNumber, trip.StoreID, de.DEID); err != nil {
		return op.Fail(err)
	}
	return nil
}

// ownedTrip fetches a trip and verifies the caller (by phone) owns it.
func (s *TripService) ownedTrip(ctx context.Context, op *logging.Op, tripID, callerDEPhone string) (*models.Trip, *models.DeliveryExecutive, error) {
	trip, err := s.tripRepo.GetByID(ctx, tripID)
	if err != nil {
		return nil, nil, op.Fail(err)
	}
	if trip == nil {
		return nil, nil, op.Outcome("not_found", fmt.Errorf("%w: %s", ErrTripNotFound, tripID))
	}
	de, err := s.deRepo.GetByPhone(ctx, callerDEPhone)
	if err != nil {
		return nil, nil, op.Fail(err)
	}
	if de == nil || trip.DEID != de.DEID {
		return nil, nil, op.Outcome("forbidden", ErrTripForbidden)
	}
	return trip, de, nil
}
```

NOTE: `logging.Start` returns `*logging.Op` (see `internal/logging/op.go`), so `ownedTrip` takes `op *logging.Op`. The two callers pass their own `op`.

- [ ] **Step 6: Run tests + build to verify pass**

Run: `go build ./... 2>&1 | grep -v assignment_cron; go test ./internal/service/ -run 'TestValidateTaskAgainstTripStatus|TestValidateTaskTransition' -v`
Expected: the new + existing validator tests PASS. (`assignment_cron.go` still won't compile until Task 5 — that's expected.)

- [ ] **Step 7: Commit**

```bash
git add internal/service/trip_service.go internal/service/trip_service_test.go
git commit -m "feat(trip-svc): add AcceptTrip/RejectTrip + enforce trip-status gate on task completion"
```

---

## Task 5: Assignment cron — config, deadline stamp, auto-reject sweep, skip-rejected

**Files:**
- Modify: `internal/service/assignment_cron.go`
- Modify: `cmd/server/main.go` (cron construction)
- Test: `internal/service/assignment_cron_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/service/assignment_cron_test.go`:

```go
import "time" // add to existing import block if not present

func TestIsAcceptExpired(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

	expired := &models.Trip{
		Status:         models.TripStatusAssigned,
		AcceptDeadline: now.Add(-1 * time.Second).Format(time.RFC3339),
	}
	if !isAcceptExpired(expired, now) {
		t.Error("expected expired assigned trip to be auto-rejectable")
	}

	future := &models.Trip{
		Status:         models.TripStatusAssigned,
		AcceptDeadline: now.Add(30 * time.Second).Format(time.RFC3339),
	}
	if isAcceptExpired(future, now) {
		t.Error("expected future-deadline trip to NOT be expired")
	}

	accepted := &models.Trip{
		Status:         models.TripStatusAccepted,
		AcceptDeadline: now.Add(-1 * time.Second).Format(time.RFC3339),
	}
	if isAcceptExpired(accepted, now) {
		t.Error("expected accepted trip to never be auto-rejected")
	}

	noDeadline := &models.Trip{Status: models.TripStatusAssigned}
	if isAcceptExpired(noDeadline, now) {
		t.Error("expected trip with no deadline to NOT be expired")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/service/ -run TestIsAcceptExpired -v`
Expected: FAIL — `undefined: isAcceptExpired`.

- [ ] **Step 3: Add the `isAcceptExpired` helper**

Append to `internal/service/assignment_cron.go`:

```go
// isAcceptExpired reports whether an assigned trip's accept window has elapsed
// and it should be auto-rejected back into the pool.
func isAcceptExpired(trip *models.Trip, now time.Time) bool {
	if trip.Status != models.TripStatusAssigned || trip.AcceptDeadline == "" {
		return false
	}
	deadline, err := time.Parse(time.RFC3339, trip.AcceptDeadline)
	if err != nil {
		return false
	}
	return now.After(deadline)
}
```

- [ ] **Step 4: Run the helper test to verify pass**

Run: `go test ./internal/service/ -run TestIsAcceptExpired -v`
Expected: PASS.

- [ ] **Step 5: Add the config repo to the cron struct and constructor**

In `internal/service/assignment_cron.go`, add the field to `AssignmentCron`:

```go
	payoutConfigRepo     *repository.PayoutConfigRepository
	assignmentConfigRepo *repository.AssignmentConfigRepository
```

Add the parameter to `NewAssignmentCron` (after `payoutConfigRepo`):

```go
	payoutConfigRepo *repository.PayoutConfigRepository,
	assignmentConfigRepo *repository.AssignmentConfigRepository,
```

and set it in the returned struct:

```go
		payoutConfigRepo:     payoutConfigRepo,
		assignmentConfigRepo: assignmentConfigRepo,
```

- [ ] **Step 6: Fetch the accept window, sweep expired trips, and rewrite the assign loop**

In `tick(ctx)`, after the payout-config fetch (step 1), add the assignment-config fetch:

```go
	acfg, err := c.assignmentConfigRepo.Get(ctx)
	if err != nil {
		c.logger.WithError(err).Warn("assignment cron: failed to fetch assignment config — using default accept window")
		acfg = &models.AssignmentConfig{}
	}
	autoRejectSecs := acfg.EffectiveAutoRejectSeconds()
```

After the `results` are built and `detectCancellations` is called (after current step 5), insert the auto-reject sweep:

```go
	// 5b. Auto-reject trips whose accept window expired — revert to pool so the
	// assign step below re-offers them (to a different DE, since the rejecter is
	// now in rejected_de_ids).
	now := timezone.Now()
	for i := range results {
		t := results[i].trip
		if t == nil || !isAcceptExpired(t, now) {
			continue
		}
		if err := c.tripRepo.RejectToPool(ctx, t.TripID, t.DEPhone, t.StoreID, t.DEID); err != nil {
			c.logger.WithError(err).WithField("trip_id", t.TripID).
				Warn("assignment cron: auto-reject failed — will retry next tick")
			continue
		}
		c.logger.WithFields(logrus.Fields{"trip_id": t.TripID, "de_id": t.DEID}).
			Info("assignment cron: auto-rejected expired trip")
		// Reflect the new pool state locally so the assign loop picks it up this
		// tick and skips the DE that just lost it.
		t.RejectedDEIDs = append(t.RejectedDEIDs, t.DEID)
		t.Status = models.TripStatusCreated
		t.DEID = ""
		t.DEPhone = ""
		t.AcceptDeadline = ""
	}
```

Then replace the entire **step 7 assign loop** (from `// Assign one DE per unassigned trip` / `deIdx := 0` through the end of the `for _, trip := range unassigned` loop) with a version that skips rejecters and tracks used DEs:

```go
	// Assign each unassigned trip to the first eligible DE that has not already
	// rejected it and is not already taken this tick.
	usedDE := make(map[string]bool)
	for _, trip := range unassigned {
		for _, de := range eligibleDEs {
			if usedDE[de.DEID] || trip.HasRejected(de.DEID) {
				continue
			}
			deadline := now.Add(time.Duration(autoRejectSecs) * time.Second).Format(time.RFC3339)
			if err := c.tripRepo.Assign(ctx, trip.TripID, trip.OrderID, de.DEID, de.PhoneNumber, now.Format(time.RFC3339), deadline); err != nil {
				c.logger.WithError(err).WithFields(logrus.Fields{
					"trip_id": trip.TripID, "de_id": de.DEID,
				}).Warn("assignment cron: assign conflict — trip or DE taken, trying next DE")
				continue
			}
			usedDE[de.DEID] = true
			c.logger.WithFields(logrus.Fields{
				"trip_id": trip.TripID, "de_id": de.DEID,
			}).Info("assignment cron: trip assigned")
			break
		}
	}
```

NOTE: the old loop computed `assignedAt` with `timezone.Now()` inline; we now reuse the `now` captured for the sweep. The previous `deIdx`-based loop is fully removed.

- [ ] **Step 7: Update the cron construction in `main.go`**

First construct the repo. In `cmd/server/main.go`, after the `payoutConfigRepo := ...` line (line 52), add:

```go
	assignmentConfigRepo := repository.NewAssignmentConfigRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
```

Then update the `NewAssignmentCron(...)` call (line 85) to pass it after `payoutConfigRepo`:

```go
	assignmentCron := service.NewAssignmentCron(tripRepo, deRepo, cronLockRepo, payoutConfigRepo, assignmentConfigRepo, darkstoreRepo, javaOrderClient, distanceService, logger)
```

- [ ] **Step 8: Build + run all service/model tests**

Run: `go build ./... && go test ./internal/service/ ./internal/models/ -v`
Expected: build OK; all tests PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/service/assignment_cron.go internal/service/assignment_cron_test.go cmd/server/main.go
git commit -m "feat(cron): stamp accept_deadline, auto-reject expired trips, skip rejecters on re-assign"
```

---

## Task 6: Driver accept/reject handlers + routes

**Files:**
- Modify: `internal/handlers/trip_handlers.go`
- Modify: `cmd/server/main.go` (routes)
- Test: `internal/handlers/trip_handlers_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/handlers/trip_handlers_test.go`:

```go
func TestClassifyAcceptRejectError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"not found", fmt.Errorf("%w: t-1", service.ErrTripNotFound), http.StatusNotFound, "NOT_FOUND"},
		{"forbidden", service.ErrTripForbidden, http.StatusForbidden, "FORBIDDEN"},
		{"invalid state", fmt.Errorf("%w: from accepted", service.ErrInvalidTripTransition), http.StatusConflict, "INVALID_TRIP_STATE"},
		{"unknown defaults 500", errors.New("boom"), http.StatusInternalServerError, "ACTION_FAILED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code := classifyAcceptRejectError(tc.err)
			if status != tc.wantStatus {
				t.Errorf("status: got %d, want %d", status, tc.wantStatus)
			}
			if code != tc.wantCode {
				t.Errorf("code: got %q, want %q", code, tc.wantCode)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/ -run TestClassifyAcceptRejectError -v`
Expected: FAIL — `undefined: classifyAcceptRejectError`.

- [ ] **Step 3: Add the handlers and classifier**

Append to `internal/handlers/trip_handlers.go`:

```go
// POST /api/v1/trip/{tripId}/accept
func (h *TripHandlers) AcceptTrip(w http.ResponseWriter, r *http.Request) {
	h.acceptOrReject(w, r, true)
}

// POST /api/v1/trip/{tripId}/reject
func (h *TripHandlers) RejectTrip(w http.ResponseWriter, r *http.Request) {
	h.acceptOrReject(w, r, false)
}

func (h *TripHandlers) acceptOrReject(w http.ResponseWriter, r *http.Request, accept bool) {
	tripID := mux.Vars(r)["tripId"]
	phone, _ := r.Context().Value("phone").(string)

	if strings.TrimSpace(tripID) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "tripId is required")
		return
	}

	var err error
	if accept {
		err = h.tripService.AcceptTrip(r.Context(), tripID, phone)
	} else {
		err = h.tripService.RejectTrip(r.Context(), tripID, phone)
	}
	if err != nil {
		status, code := classifyAcceptRejectError(err)
		if status == http.StatusInternalServerError {
			h.logger.WithError(err).Error("failed to accept/reject trip")
			h.respondWithError(w, status, code, "Failed to update trip")
			return
		}
		h.respondWithError(w, status, code, err.Error())
		return
	}

	result := "accepted"
	if !accept {
		result = "rejected"
	}
	h.respondWithJSON(w, http.StatusOK, map[string]string{"status": result})
}

// classifyAcceptRejectError maps TripService.AcceptTrip/RejectTrip errors to
// HTTP status codes using errors.Is against the service sentinels.
func classifyAcceptRejectError(err error) (status int, code string) {
	switch {
	case errors.Is(err, service.ErrTripNotFound):
		return http.StatusNotFound, "NOT_FOUND"
	case errors.Is(err, service.ErrTripForbidden):
		return http.StatusForbidden, "FORBIDDEN"
	case errors.Is(err, service.ErrInvalidTripTransition):
		return http.StatusConflict, "INVALID_TRIP_STATE"
	default:
		return http.StatusInternalServerError, "ACTION_FAILED"
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/handlers/ -run TestClassifyAcceptRejectError -v`
Expected: PASS.

- [ ] **Step 5: Register the routes**

In `cmd/server/main.go`, in the `tripRoutes` subrouter block (around line 305, which already has `RequireDEAuth`), add after the existing task-status route:

```go
	tripRoutes.HandleFunc("/{tripId}/accept", tripHandlers.AcceptTrip).Methods("POST", "OPTIONS")
	tripRoutes.HandleFunc("/{tripId}/reject", tripHandlers.RejectTrip).Methods("POST", "OPTIONS")
```

- [ ] **Step 6: Build + test**

Run: `go build ./... && go test ./internal/handlers/ -v`
Expected: build OK; tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/handlers/trip_handlers.go internal/handlers/trip_handlers_test.go cmd/server/main.go
git commit -m "feat(trip-api): add POST /trip/{id}/accept and /trip/{id}/reject endpoints"
```

---

## Task 7: Admin assign — service + handler + route

**Files:**
- Create: `internal/service/admin_service.go`
- Create: `internal/handlers/admin_handlers.go`
- Modify: `cmd/server/main.go` (DI + route)
- Test: `internal/handlers/admin_handlers_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/handlers/admin_handlers_test.go`:

```go
package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/qcom/qcom/internal/service"
)

func TestClassifyAdminAssignError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"trip not found", fmt.Errorf("%w: o-1", service.ErrTripNotFound), http.StatusNotFound, "ORDER_NOT_FOUND"},
		{"not assignable", fmt.Errorf("%w: assigned", service.ErrInvalidTripTransition), http.StatusConflict, "ORDER_NOT_ASSIGNABLE"},
		{"de not found", service.ErrDENotFound, http.StatusNotFound, "DRIVER_NOT_FOUND"},
		{"de not eligible", service.ErrDENotEligible, http.StatusConflict, "DRIVER_NOT_ELIGIBLE"},
		{"unknown 500", errors.New("boom"), http.StatusInternalServerError, "ASSIGN_FAILED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code := classifyAdminAssignError(tc.err)
			if status != tc.wantStatus {
				t.Errorf("status: got %d, want %d", status, tc.wantStatus)
			}
			if code != tc.wantCode {
				t.Errorf("code: got %q, want %q", code, tc.wantCode)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/ -run TestClassifyAdminAssignError -v`
Expected: FAIL — `undefined: service.ErrDENotFound`, `classifyAdminAssignError`.

- [ ] **Step 3: Create the admin service**

Create `internal/service/admin_service.go`:

```go
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

var (
	ErrDENotFound    = errors.New("driver not found")
	ErrDENotEligible = errors.New("driver not eligible")
)

// AdminService force-assigns pooled orders to drivers (ops escape hatch).
// Auth is assumed to be handled upstream of these calls.
type AdminService struct {
	tripRepo *repository.TripRepository
	deRepo   *repository.DERepository
	logger   *logrus.Logger
}

func NewAdminService(tripRepo *repository.TripRepository, deRepo *repository.DERepository, logger *logrus.Logger) *AdminService {
	return &AdminService{tripRepo: tripRepo, deRepo: deRepo, logger: logger}
}

// AssignOrder force-assigns the trip for orderID directly to accepted for the
// driver identified by driverPhone. Preconditions: trip is created (pooled),
// driver is eligible (online + on duty, not busy). Bypasses rejected_de_ids.
func (s *AdminService) AssignOrder(ctx context.Context, orderID, driverPhone string) error {
	op := logging.Start(ctx, s.logger, "AdminService.AssignOrder", logrus.Fields{
		"order_id": orderID, "driver_phone": driverPhone,
	})
	defer op.End()

	trip, err := s.tripRepo.GetByOrderID(ctx, orderID)
	if err != nil {
		return op.Fail(err)
	}
	if trip == nil {
		return op.Outcome("not_found", fmt.Errorf("%w: order %s", ErrTripNotFound, orderID))
	}
	if trip.Status != models.TripStatusCreated {
		return op.Outcome("not_assignable", fmt.Errorf("%w: order not in pool (status=%s)", ErrInvalidTripTransition, trip.Status))
	}

	de, err := s.deRepo.GetByPhone(ctx, driverPhone)
	if err != nil {
		return op.Fail(err)
	}
	if de == nil {
		return op.Outcome("de_not_found", fmt.Errorf("%w: %s", ErrDENotFound, driverPhone))
	}
	if de.Status != models.DEStatusEligible {
		return op.Outcome("de_not_eligible", fmt.Errorf("%w: driver status=%s", ErrDENotEligible, de.Status))
	}

	if err := s.tripRepo.AdminAssign(ctx, trip.TripID, trip.OrderID, de.DEID, de.PhoneNumber, trip.StoreID); err != nil {
		return op.Fail(err)
	}
	return nil
}
```

- [ ] **Step 4: Create the admin handler**

Create `internal/handlers/admin_handlers.go`:

```go
package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type AdminHandlers struct {
	adminService *service.AdminService
	logger       *logrus.Logger
}

func NewAdminHandlers(adminService *service.AdminService, logger *logrus.Logger) *AdminHandlers {
	return &AdminHandlers{adminService: adminService, logger: logger}
}

// POST /api/v1/admin/assign
// Body: { "order_id": "...", "driver_phone": "..." }
func (h *AdminHandlers) AssignOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OrderID     string `json:"order_id"`
		DriverPhone string `json:"driver_phone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if strings.TrimSpace(req.OrderID) == "" || strings.TrimSpace(req.DriverPhone) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "order_id and driver_phone are required")
		return
	}

	if err := h.adminService.AssignOrder(r.Context(), req.OrderID, req.DriverPhone); err != nil {
		status, code := classifyAdminAssignError(err)
		if status == http.StatusInternalServerError {
			h.logger.WithError(err).Error("admin assign failed")
			h.respondWithError(w, status, code, "Failed to assign order")
			return
		}
		h.respondWithError(w, status, code, err.Error())
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]string{"status": "assigned"})
}

func classifyAdminAssignError(err error) (status int, code string) {
	switch {
	case errors.Is(err, service.ErrTripNotFound):
		return http.StatusNotFound, "ORDER_NOT_FOUND"
	case errors.Is(err, service.ErrInvalidTripTransition):
		return http.StatusConflict, "ORDER_NOT_ASSIGNABLE"
	case errors.Is(err, service.ErrDENotFound):
		return http.StatusNotFound, "DRIVER_NOT_FOUND"
	case errors.Is(err, service.ErrDENotEligible):
		return http.StatusConflict, "DRIVER_NOT_ELIGIBLE"
	default:
		return http.StatusInternalServerError, "ASSIGN_FAILED"
	}
}

func (h *AdminHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func (h *AdminHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{
		Error: ErrorDetail{Code: code, Message: message},
	})
}
```

NOTE: `ErrorResponse`/`ErrorDetail` already exist in the `handlers` package (used by `trip_handlers.go`); reuse them, do not redefine.

- [ ] **Step 5: Wire DI + route in `main.go`**

In `cmd/server/main.go`, after `tripService := ...` (line 83), add:

```go
	adminService := service.NewAdminService(tripRepo, deRepo, logger)
```

Near the other handler constructors (around line 110), add:

```go
	adminHandlers := handlers.NewAdminHandlers(adminService, logger)
```

Pass `adminHandlers` into `setupRouter` — add a parameter to the `setupRouter` function signature and the call site (line 117), mirroring how `tripHandlers` is threaded through. Then register the route in the `api` subrouter (auth assumed upstream — no middleware):

```go
	admin := api.PathPrefix("/admin").Subrouter()
	admin.HandleFunc("/assign", adminHandlers.AssignOrder).Methods("POST", "OPTIONS")
```

- [ ] **Step 6: Build + test**

Run: `go build ./... && go test ./internal/handlers/ -run TestClassifyAdminAssignError -v`
Expected: build OK; test PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/service/admin_service.go internal/handlers/admin_handlers.go internal/handlers/admin_handlers_test.go cmd/server/main.go
git commit -m "feat(admin): add POST /admin/assign to force-assign pooled orders to eligible drivers"
```

---

## Task 8: Customer track API — hide the accept window

**Files:**
- Modify: `internal/handlers/track_handlers.go`

The customer must see `finding_driver` for both `created` and `assigned`, and learn the driver (name + OTP) only once the trip is `accepted`. Completed/cancelled handling is unchanged (`completed` stays `completed`).

- [ ] **Step 1: Gate driver reveal on accepted-or-later**

In `internal/handlers/track_handlers.go`, replace the DE-name block (lines ~103–112) so the name only appears once the driver has committed:

```go
	// Driver is revealed to the customer only once they have accepted — never
	// during the assigned (pending-accept) window, which may end in a reject.
	committed := trip.Status == models.TripStatusAccepted || trip.Status == models.TripStatusOutForDelivery
	if committed && trip.DEPhone != "" {
		de, err := h.deRepo.GetByPhone(r.Context(), trip.DEPhone)
		if err == nil && de != nil {
			response.DEName = &de.Name
		}
	}
```

NOTE: this also fixes the prior known bug — the DE was looked up by `trip.DEID` (a uuid), but the DE PK is the phone. We now use `trip.DEPhone`, stamped at assignment (Task 3).

- [ ] **Step 2: Gate OTP on committed too**

Replace the OTP block (lines ~114–122) so the OTP isn't leaked before the driver commits:

```go
	// OTP — shown only once the driver has committed and the drop is still open.
	dropTask := trip.DropTask()
	if committed && dropTask != nil && dropTask.Status == models.TaskStatusCreated {
		otp := dropTask.OTP
		response.OTP = &otp
	}
```

- [ ] **Step 3: Report finding_driver through the whole accept window**

Replace the final status-massage block (lines ~129–132):

```go
	// Customer sees "finding_driver" until a driver accepts — the assigned
	// (pending-accept) window and any reject churn stay invisible.
	if trip.Status == models.TripStatusCreated || trip.Status == models.TripStatusAssigned {
		response.TripStatus = "finding_driver"
	}
```

- [ ] **Step 4: Build + run handler tests**

Run: `go build ./... && go test ./internal/handlers/ -v`
Expected: build OK; tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/track_handlers.go
git commit -m "feat(track): hide pending-accept window from customer; reveal driver only after accept"
```

---

## Final verification

- [ ] **Run the full unit suite**

Run: `go build ./... && go test ./...`
Expected: build OK; all non-integration tests PASS.

- [ ] **Optional: run the integration suite if the harness is available**

Run: `go test -tags integration ./tests/integration/ -run TestTripProgressionFlow -v`
Expected: PASS or SKIP (skips if Java has no PACKING order for the test store). If it runs, confirm the flow now requires accept before pickup — the test may need a `POST /api/v1/trip/{tripId}/accept` call inserted between "trip assigned" and "pickup complete". If so, update `tests/integration/trip_progression_test.go` accordingly and commit separately.

---

## Self-review checklist (completed by plan author)

- **Spec coverage:** accepted status ✓ (T1), rename in_transit→out_for_delivery ✓ (T1), strict state machine + task gate ✓ (T1, T4), accept/reject endpoints ✓ (T6), reject→pool + DE back to eligible ✓ (T3, T4), auto-reject via deadline+cron ✓ (T3, T5), skip rejecters / park if all rejected ✓ (T5), admin assign ✓ (T7), AssignmentConfig default 60 ✓ (T2), Java untouched ✓ (no Java task), customer hides accept window ✓ (T8).
- **Type consistency:** `TripStatusOutForDelivery`, `TripStatusAccepted`, `Trip.DEPhone`, `Trip.AcceptDeadline`, `Trip.RejectedDEIDs`, `Assign(...,acceptDeadline)`, `Accept`, `RejectToPool`, `AdminAssign`, `AcceptTrip`/`RejectTrip`, `AssignOrder`, `*logging.Op`, sentinels `ErrInvalidTripTransition`/`ErrDENotFound`/`ErrDENotEligible` are used consistently across tasks.
```
