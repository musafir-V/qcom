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

type TripReachedConfigRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewTripReachedConfigRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *TripReachedConfigRepository {
	return &TripReachedConfigRepository{client: client, tableName: tableName, logger: logger}
}

// Get returns the config. Missing row → zero value (handler applies 150m / false).
func (r *TripReachedConfigRepository) Get(ctx context.Context) (*models.TripReachedConfig, error) {
	op := logging.Start(ctx, r.logger, "TripReachedConfigRepository.Get", nil)
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "CONFIG"},
			"SK": &types.AttributeValueMemberS{Value: "TRIP_REACHED_V1"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get trip reached config: %w", err))
	}
	if result.Item == nil {
		return &models.TripReachedConfig{}, nil
	}
	var cfg models.TripReachedConfig
	if err := attributevalue.UnmarshalMap(result.Item, &cfg); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal trip reached config: %w", err))
	}
	return &cfg, nil
}

// Put upserts PK=CONFIG SK=TRIP_REACHED_V1.
func (r *TripReachedConfigRepository) Put(ctx context.Context, cfg *models.TripReachedConfig) error {
	op := logging.Start(ctx, r.logger, "TripReachedConfigRepository.Put", logrus.Fields{
		"radius_meters":                   cfg.RadiusMeters,
		"require_reached_before_complete": cfg.RequireReachedBeforeComplete,
	})
	defer op.End()

	item, err := attributevalue.MarshalMap(cfg)
	if err != nil {
		return op.Fail(fmt.Errorf("marshal trip reached config: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: cfg.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: cfg.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return op.Fail(fmt.Errorf("put trip reached config: %w", err))
	}
	return nil
}
