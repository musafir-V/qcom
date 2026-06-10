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
