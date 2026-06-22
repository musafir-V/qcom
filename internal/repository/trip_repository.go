package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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
func (r *TripRepository) Assign(
	ctx context.Context,
	tripID, orderID, deID, dePhone, assignedAt, acceptDeadline string,
	slaMinutes float64,
	rateRuleID string,
	rateRuleVersion int,
	rateMultiplier, rateFlatZMW float64,
) error {
	op := logging.Start(ctx, r.logger, "TripRepository.Assign", logrus.Fields{
		"trip_id": tripID, "de_id": deID,
	})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)

	tripUpdateExpr := "SET #status = :assigned, de_id = :de_id, de_phone = :de_phone, assigned_at = :at, accept_deadline = :deadline, sla_minutes = :sla, rate_multiplier = :mult, rate_flat_zmw = :flat, updated_at = :now"
	if rateRuleID != "" {
		tripUpdateExpr += ", rate_rule_id = :rule_id, rate_rule_version = :rule_version"
	} else {
		tripUpdateExpr += " REMOVE rate_rule_id, rate_rule_version"
	}

	tripExprValues := map[string]types.AttributeValue{
		":assigned": &types.AttributeValueMemberS{Value: string(models.TripStatusAssigned)},
		":de_id":    &types.AttributeValueMemberS{Value: deID},
		":de_phone": &types.AttributeValueMemberS{Value: dePhone},
		":at":       &types.AttributeValueMemberS{Value: assignedAt},
		":deadline": &types.AttributeValueMemberS{Value: acceptDeadline},
		":sla":      &types.AttributeValueMemberN{Value: strconv.FormatFloat(slaMinutes, 'f', 4, 64)},
		":mult":     &types.AttributeValueMemberN{Value: strconv.FormatFloat(rateMultiplier, 'f', 4, 64)},
		":flat":     &types.AttributeValueMemberN{Value: strconv.FormatFloat(rateFlatZMW, 'f', 2, 64)},
		":now":      &types.AttributeValueMemberS{Value: now},
	}
	if rateRuleID != "" {
		tripExprValues[":rule_id"] = &types.AttributeValueMemberS{Value: rateRuleID}
		tripExprValues[":rule_version"] = &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", rateRuleVersion)}
	}

	_, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				Update: &types.Update{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: "TRIP!" + tripID},
						"SK": &types.AttributeValueMemberS{Value: "METADATA"},
					},
					UpdateExpression:         aws.String(tripUpdateExpr),
					ConditionExpression:      aws.String("attribute_not_exists(de_id)"),
					ExpressionAttributeNames: map[string]string{"#status": "status"},
					ExpressionAttributeValues: tripExprValues,
				},
			},
			{
				Update: &types.Update{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: "DE!" + dePhone},
						"SK": &types.AttributeValueMemberS{Value: "METADATA"},
					},
					UpdateExpression:         aws.String("SET #status = :busy, current_order_id = :oid, current_trip_id = :tid, updated_at = :now REMOVE duty_index_key"),
					ConditionExpression:      aws.String("#status = :eligible"),
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

// CompleteTripAndFreeDE atomically marks the trip completed (with final tasks),
// frees the assigned DE, and accrues the COD cash amount to the DE's in-hand
// balance. The DE must be busy on this trip.
func (r *TripRepository) CompleteTripAndFreeDE(ctx context.Context, tripID, dePhone string, tasks []models.Task, codAmount float64) error {
	op := logging.Start(ctx, r.logger, "TripRepository.CompleteTripAndFreeDE", logrus.Fields{
		"trip_id": tripID, "de_phone": dePhone, "cod_amount_zmw": codAmount,
	})
	defer op.End()

	tasksAttr, err := attributevalue.Marshal(tasks)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal tasks: %w", err))
	}

	now := time.Now().UTC().Format(time.RFC3339)

	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				Update: &types.Update{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: "TRIP!" + tripID},
						"SK": &types.AttributeValueMemberS{Value: "METADATA"},
					},
					UpdateExpression: aws.String("SET tasks = :tasks, #status = :completed, completed_at = :now, updated_at = :now"),
					ConditionExpression: aws.String(
						"#status <> :completed AND #status <> :cancelled",
					),
					ExpressionAttributeNames: map[string]string{"#status": "status"},
					ExpressionAttributeValues: map[string]types.AttributeValue{
						":tasks":     tasksAttr,
						":completed": &types.AttributeValueMemberS{Value: string(models.TripStatusCompleted)},
						":cancelled": &types.AttributeValueMemberS{Value: string(models.TripStatusCancelled)},
						":now":       &types.AttributeValueMemberS{Value: now},
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
						"SET #status = :free, updated_at = :now, in_hand_cash_zmw = if_not_exists(in_hand_cash_zmw, :zero) + :cod " +
							"REMOVE current_order_id, current_trip_id, current_store_id, duty_index_key",
					),
					ConditionExpression: aws.String("#status = :busy AND current_trip_id = :tid"),
					ExpressionAttributeNames: map[string]string{"#status": "status"},
					ExpressionAttributeValues: map[string]types.AttributeValue{
						":free": &types.AttributeValueMemberS{Value: string(models.DEStatusFree)},
						":busy": &types.AttributeValueMemberS{Value: string(models.DEStatusBusy)},
						":tid":  &types.AttributeValueMemberS{Value: tripID},
						":now":  &types.AttributeValueMemberS{Value: now},
						// FormatFloat with precision -1 preserves fractional ZMW (e.g. 482.50); the
						// payout fields elsewhere use "%.0f" because they round to whole kwacha.
						":cod":  &types.AttributeValueMemberN{Value: strconv.FormatFloat(codAmount, 'f', -1, 64)},
						":zero": &types.AttributeValueMemberN{Value: "0"},
					},
				},
			},
		},
	})
	if err != nil {
		var txErr *types.TransactionCanceledException
		if errors.As(err, &txErr) {
			return op.Outcome("conflict", fmt.Errorf("trip completion conflict: trip closed or DE not busy on this trip"))
		}
		return op.Fail(fmt.Errorf("failed to complete trip and free DE: %w", err))
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
			":base":  &types.AttributeValueMemberN{Value: fmt.Sprintf("%.0f", basePayZMW)},
			":bonus": &types.AttributeValueMemberN{Value: fmt.Sprintf("%.0f", bonusPayZMW)},
			":total": &types.AttributeValueMemberN{Value: fmt.Sprintf("%.0f", totalPayZMW)},
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
				UpdateExpression:         aws.String("SET #status = :cancelled, cancelled_at = :now, updated_at = :now"),
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
				UpdateExpression:         aws.String("SET #status = :free, updated_at = :now REMOVE current_order_id, current_trip_id, duty_index_key"),
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
