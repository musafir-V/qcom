// internal/repository/dispute_disposition_repository.go
package repository

import (
	"context"
	"fmt"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type DisputeDispositionRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewDisputeDispositionRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *DisputeDispositionRepository {
	return &DisputeDispositionRepository{client: client, tableName: tableName, logger: logger}
}

// ListActive returns active dispositions sorted by DisplayOrder.
func (r *DisputeDispositionRepository) ListActive(ctx context.Context) ([]models.DisputeDisposition, error) {
	out, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		KeyConditionExpression: aws.String("PK = :pk AND begins_with(SK, :prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":     &types.AttributeValueMemberS{Value: models.DisputeConfigPK},
			":prefix": &types.AttributeValueMemberS{Value: "DISPUTE_DISPOSITION!"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query dispositions: %w", err)
	}
	var all []models.DisputeDisposition
	for _, item := range out.Items {
		var d models.DisputeDisposition
		if err := attributevalue.UnmarshalMap(item, &d); err != nil {
			r.logger.WithError(err).Warn("failed to unmarshal disposition; skipping")
			continue
		}
		if d.Active {
			all = append(all, d)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].DisplayOrder < all[j].DisplayOrder })
	return all, nil
}

func (r *DisputeDispositionRepository) GetByCode(ctx context.Context, code string) (*models.DisputeDisposition, error) {
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: models.DisputeConfigPK},
			"SK": &types.AttributeValueMemberS{Value: models.DisputeDispositionSK(code)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get disposition %q: %w", code, err)
	}
	if out.Item == nil {
		return nil, nil
	}
	var d models.DisputeDisposition
	if err := attributevalue.UnmarshalMap(out.Item, &d); err != nil {
		return nil, fmt.Errorf("failed to unmarshal disposition: %w", err)
	}
	return &d, nil
}
