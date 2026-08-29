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

type DropDeadlineConfigRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewDropDeadlineConfigRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *DropDeadlineConfigRepository {
	return &DropDeadlineConfigRepository{client: client, tableName: tableName, logger: logger}
}

// Get returns the config. Missing row → zero value (handler applies x=2 / y=0).
func (r *DropDeadlineConfigRepository) Get(ctx context.Context) (*models.DropDeadlineConfig, error) {
	op := logging.Start(ctx, r.logger, "DropDeadlineConfigRepository.Get", nil)
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "CONFIG"},
			"SK": &types.AttributeValueMemberS{Value: "DROP_DEADLINE_V1"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get drop deadline config: %w", err))
	}
	if result.Item == nil {
		return &models.DropDeadlineConfig{}, nil
	}
	var cfg models.DropDeadlineConfig
	if err := attributevalue.UnmarshalMap(result.Item, &cfg); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal drop deadline config: %w", err))
	}
	return &cfg, nil
}

// Put upserts PK=CONFIG SK=DROP_DEADLINE_V1.
func (r *DropDeadlineConfigRepository) Put(ctx context.Context, cfg *models.DropDeadlineConfig) error {
	op := logging.Start(ctx, r.logger, "DropDeadlineConfigRepository.Put", logrus.Fields{
		"minutes_per_km": cfg.MinutesPerKm,
		"extra_minutes":  cfg.ExtraMinutes,
	})
	defer op.End()

	item, err := attributevalue.MarshalMap(cfg)
	if err != nil {
		return op.Fail(fmt.Errorf("marshal drop deadline config: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: cfg.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: cfg.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return op.Fail(fmt.Errorf("put drop deadline config: %w", err))
	}
	return nil
}
