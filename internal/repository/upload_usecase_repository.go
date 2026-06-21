package repository

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type UploadUseCaseRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewUploadUseCaseRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *UploadUseCaseRepository {
	return &UploadUseCaseRepository{client: client, tableName: tableName, logger: logger}
}

// GetByUseCase returns the registry entry, or (nil, nil) if it does not exist.
func (r *UploadUseCaseRepository) GetByUseCase(ctx context.Context, useCase string) (*models.UploadUseCase, error) {
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: models.DisputeConfigPK},
			"SK": &types.AttributeValueMemberS{Value: models.UploadUseCaseSK(useCase)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get upload use case %q: %w", useCase, err)
	}
	if out.Item == nil {
		return nil, nil
	}
	var entry models.UploadUseCase
	if err := attributevalue.UnmarshalMap(out.Item, &entry); err != nil {
		return nil, fmt.Errorf("failed to unmarshal upload use case: %w", err)
	}
	return &entry, nil
}
