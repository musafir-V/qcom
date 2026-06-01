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

type PayoutConfigRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewPayoutConfigRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *PayoutConfigRepository {
	return &PayoutConfigRepository{client: client, tableName: tableName, logger: logger}
}

func (r *PayoutConfigRepository) Get(ctx context.Context) (*models.PayoutConfig, error) {
	op := logging.Start(ctx, r.logger, "PayoutConfigRepository.Get", nil)
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "CONFIG"},
			"SK": &types.AttributeValueMemberS{Value: "PAYOUT_V1"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get payout config: %w", err))
	}
	if result.Item == nil {
		return nil, op.Outcome("not_found", fmt.Errorf("payout config not found"))
	}

	var cfg models.PayoutConfig
	if err := attributevalue.UnmarshalMap(result.Item, &cfg); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal payout config: %w", err))
	}
	return &cfg, nil
}

// UpdateField updates a single attribute in the payout config item.
// fieldName must match the DynamoDB attribute name (snake_case).
// value must be a valid DynamoDB AttributeValue.
func (r *PayoutConfigRepository) UpdateField(ctx context.Context, fieldName string, value types.AttributeValue) error {
	op := logging.Start(ctx, r.logger, "PayoutConfigRepository.UpdateField", logrus.Fields{"field": fieldName})
	defer op.End()

	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "CONFIG"},
			"SK": &types.AttributeValueMemberS{Value: "PAYOUT_V1"},
		},
		UpdateExpression: aws.String("SET #field = :value"),
		ExpressionAttributeNames: map[string]string{
			"#field": fieldName,
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":value": value,
		},
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to update payout config field %s: %w", fieldName, err))
	}
	return nil
}
