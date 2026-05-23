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

type DarkstoreRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewDarkstoreRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *DarkstoreRepository {
	return &DarkstoreRepository{
		client:    client,
		tableName: tableName,
		logger:    logger,
	}
}

// ListActive returns every active darkstore. Darkstores are few and rarely
// change, so a full Scan per request is acceptable for v1.
func (r *DarkstoreRepository) ListActive(ctx context.Context) ([]models.Darkstore, error) {
	var darkstores []models.Darkstore
	var startKey map[string]types.AttributeValue

	for {
		result, err := r.client.Scan(ctx, &dynamodb.ScanInput{
			TableName:        aws.String(r.tableName),
			FilterExpression: aws.String("begins_with(PK, :pk) AND SK = :sk AND is_active = :active"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk":     &types.AttributeValueMemberS{Value: "DARKSTORE!"},
				":sk":     &types.AttributeValueMemberS{Value: "METADATA"},
				":active": &types.AttributeValueMemberBOOL{Value: true},
			},
			ExclusiveStartKey: startKey,
		})
		if err != nil {
			r.logger.WithError(err).Error("Failed to scan darkstores from DynamoDB")
			return nil, fmt.Errorf("failed to scan darkstores: %w", err)
		}

		var page []models.Darkstore
		if err := attributevalue.UnmarshalListOfMaps(result.Items, &page); err != nil {
			r.logger.WithError(err).Error("Failed to unmarshal darkstore list")
			return nil, fmt.Errorf("failed to unmarshal darkstores: %w", err)
		}
		darkstores = append(darkstores, page...)

		if result.LastEvaluatedKey == nil {
			break
		}
		startKey = result.LastEvaluatedKey
	}

	return darkstores, nil
}
