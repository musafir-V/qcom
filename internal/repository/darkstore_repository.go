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

// GetByID fetches a darkstore by its darkstore_id (primary key lookup).
func (r *DarkstoreRepository) GetByID(ctx context.Context, darkstoreID string) (*models.Darkstore, error) {
	op := logging.Start(ctx, r.logger, "GetByID", logrus.Fields{"darkstore_id": darkstoreID})
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DARKSTORE!" + darkstoreID},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get darkstore: %w", err))
	}
	if result.Item == nil {
		op.With("found", false)
		return nil, nil
	}

	var ds models.Darkstore
	if err := attributevalue.UnmarshalMap(result.Item, &ds); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal darkstore: %w", err))
	}
	op.With("found", true)
	return &ds, nil
}

// ListActive returns every active darkstore. Darkstores are few and rarely
// change, so a full Scan per request is acceptable for v1.
func (r *DarkstoreRepository) ListActive(ctx context.Context) ([]models.Darkstore, error) {
	op := logging.Start(ctx, r.logger, "ListActive", nil)
	defer op.End()

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
			return nil, op.Fail(fmt.Errorf("failed to scan darkstores: %w", err))
		}

		var page []models.Darkstore
		if err := attributevalue.UnmarshalListOfMaps(result.Items, &page); err != nil {
			return nil, op.Fail(fmt.Errorf("failed to unmarshal darkstores: %w", err))
		}
		darkstores = append(darkstores, page...)

		if result.LastEvaluatedKey == nil {
			break
		}
		startKey = result.LastEvaluatedKey
	}

	op.With("count", len(darkstores))
	return darkstores, nil
}
