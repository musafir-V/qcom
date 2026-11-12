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

type BatchingConfigRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewBatchingConfigRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *BatchingConfigRepository {
	return &BatchingConfigRepository{client: client, tableName: tableName, logger: logger}
}

// Get returns the batching config. Missing row → zero value (Enabled=false).
func (r *BatchingConfigRepository) Get(ctx context.Context) (*models.BatchingConfig, error) {
	op := logging.Start(ctx, r.logger, "BatchingConfigRepository.Get", nil)
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "CONFIG"},
			"SK": &types.AttributeValueMemberS{Value: "BATCHING_V1"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get batching config: %w", err))
	}
	if result.Item == nil {
		return &models.BatchingConfig{}, nil
	}
	var cfg models.BatchingConfig
	if err := attributevalue.UnmarshalMap(result.Item, &cfg); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal batching config: %w", err))
	}
	return &cfg, nil
}
