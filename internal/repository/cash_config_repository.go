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

type CashConfigRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewCashConfigRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *CashConfigRepository {
	return &CashConfigRepository{client: client, tableName: tableName, logger: logger}
}

// Get returns the cash config. A missing row is not an error — it returns a
// zero-value config, and callers use EffectiveLimitZMW for the default.
func (r *CashConfigRepository) Get(ctx context.Context) (*models.CashConfig, error) {
	op := logging.Start(ctx, r.logger, "CashConfigRepository.Get", nil)
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "CONFIG"},
			"SK": &types.AttributeValueMemberS{Value: "CASH_V1"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get cash config: %w", err))
	}
	if result.Item == nil {
		return &models.CashConfig{}, nil
	}

	var cfg models.CashConfig
	if err := attributevalue.UnmarshalMap(result.Item, &cfg); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal cash config: %w", err))
	}
	return &cfg, nil
}
