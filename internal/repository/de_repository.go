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

type DERepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewDERepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *DERepository {
	return &DERepository{client: client, tableName: tableName, logger: logger}
}

func (r *DERepository) Create(ctx context.Context, de *models.DeliveryExecutive) error {
	op := logging.Start(ctx, r.logger, "Create", logrus.Fields{"phone": de.PhoneNumber})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)
	de.CreatedAt = now
	de.UpdatedAt = now
	de.Status = models.DEStatusOffline

	item, err := attributevalue.MarshalMap(de)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal DE: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: de.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: de.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(r.tableName),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return op.Outcome("already_exists", fmt.Errorf("delivery executive already registered with this number"))
		}
		return op.Fail(fmt.Errorf("failed to create DE: %w", err))
	}
	return nil
}

func (r *DERepository) GetByPhone(ctx context.Context, phone string) (*models.DeliveryExecutive, error) {
	op := logging.Start(ctx, r.logger, "GetByPhone", logrus.Fields{"phone": phone})
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get DE: %w", err))
	}
	if result.Item == nil {
		op.With("found", false)
		return nil, nil
	}

	var de models.DeliveryExecutive
	if err := attributevalue.UnmarshalMap(result.Item, &de); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal DE: %w", err))
	}
	de.PhoneNumber = phone

	op.With("found", true)
	return &de, nil
}

func (r *DERepository) Exists(ctx context.Context, phone string) (bool, error) {
	op := logging.Start(ctx, r.logger, "Exists", logrus.Fields{"phone": phone})
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		ProjectionExpression: aws.String("PK"),
	})
	if err != nil {
		return false, op.Fail(fmt.Errorf("failed to check DE existence: %w", err))
	}

	found := result.Item != nil
	op.With("found", found)
	return found, nil
}

// UpdateStatus transitions the DE to a new status and updates related fields.
// Pass empty strings for storeID and orderID to clear those fields.
func (r *DERepository) UpdateStatus(ctx context.Context, phone string, status models.DEStatus, storeID, orderID string) error {
	op := logging.Start(ctx, r.logger, "UpdateStatus", logrus.Fields{
		"phone":  phone,
		"status": string(status),
	})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)

	// Build duty_index_key for the DEDutyIndex GSI (used by assignment cron)
	dutyIndexKey := ""
	if status == models.DEStatusEligible && storeID != "" {
		dutyIndexKey = "DE_ELIGIBLE#" + storeID
	}

	expr := "SET #status = :status, updated_at = :updated_at"
	names := map[string]string{"#status": "status"}
	values := map[string]types.AttributeValue{
		":status":     &types.AttributeValueMemberS{Value: string(status)},
		":updated_at": &types.AttributeValueMemberS{Value: now},
	}

	if storeID != "" {
		expr += ", current_store_id = :store_id"
		values[":store_id"] = &types.AttributeValueMemberS{Value: storeID}
	} else {
		expr += " REMOVE current_store_id"
	}

	if orderID != "" {
		expr += ", current_order_id = :order_id"
		values[":order_id"] = &types.AttributeValueMemberS{Value: orderID}
	} else {
		expr += ", current_order_id = :empty_order"
		values[":empty_order"] = &types.AttributeValueMemberS{Value: ""}
	}

	if dutyIndexKey != "" {
		expr += ", duty_index_key = :duty_key"
		values[":duty_key"] = &types.AttributeValueMemberS{Value: dutyIndexKey}
	} else {
		expr += " REMOVE duty_index_key"
	}

	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression:          aws.String(expr),
		ExpressionAttributeNames:  names,
		ExpressionAttributeValues: values,
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to update DE status: %w", err))
	}
	return nil
}

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

// FindEligibleByStore returns all DEs with status=eligible at the given store.
// Uses a table scan with filter — add DEDutyIndex GSI for production scale.
func (r *DERepository) FindEligibleByStore(ctx context.Context, storeID string) ([]*models.DeliveryExecutive, error) {
	op := logging.Start(ctx, r.logger, "FindEligibleByStore", logrus.Fields{"store_id": storeID})
	defer op.End()

	dutyKey := "DE_ELIGIBLE#" + storeID

	result, err := r.client.Scan(ctx, &dynamodb.ScanInput{
		TableName:        aws.String(r.tableName),
		FilterExpression: aws.String("duty_index_key = :duty_key"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":duty_key": &types.AttributeValueMemberS{Value: dutyKey},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to find eligible DEs: %w", err))
	}

	var des []*models.DeliveryExecutive
	for _, item := range result.Items {
		var de models.DeliveryExecutive
		if err := attributevalue.UnmarshalMap(item, &de); err != nil {
			op.Logger().WithError(err).Warn("failed to unmarshal an eligible DE; skipping")
			continue
		}
		des = append(des, &de)
	}

	op.With("count", len(des))
	return des, nil
}

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
