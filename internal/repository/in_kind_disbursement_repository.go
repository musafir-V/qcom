package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/ids"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type InKindDisbursementRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
	idGen     *ids.Generator
}

func NewInKindDisbursementRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *InKindDisbursementRepository {
	return &InKindDisbursementRepository{
		client:    client,
		tableName: tableName,
		logger:    logger,
		idGen:     ids.NewGenerator(client, tableName),
	}
}

// Create writes a new in-kind disbursement record, auto-generating IDs and timestamps.
func (r *InKindDisbursementRepository) Create(ctx context.Context, d *models.InKindDisbursement) error {
	op := logging.Start(ctx, r.logger, "InKindDisbursement.Create", logrus.Fields{
		"de_id": d.DEID, "sku": string(d.SKU), "quantity": d.Quantity,
	})
	defer op.End()

	if d.DisbursedAt == "" {
		d.DisbursedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if d.DisbursementID == "" {
		id, err := r.idGen.NextID(ctx, ids.InKindDisbursement)
		if err != nil {
			return op.Fail(fmt.Errorf("failed to generate disbursement_id: %w", err))
		}
		d.DisbursementID = id
	}

	item, err := attributevalue.MarshalMap(d)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: d.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: d.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to put item: %w", err))
	}
	return nil
}

// ListByDE returns all in-kind disbursements for a DE, newest first.
func (r *InKindDisbursementRepository) ListByDE(ctx context.Context, deID string) ([]*models.InKindDisbursement, error) {
	op := logging.Start(ctx, r.logger, "InKindDisbursement.ListByDE", logrus.Fields{"de_id": deID})
	defer op.End()

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		KeyConditionExpression: aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: "INKIND_DISB!" + deID},
		},
		ScanIndexForward: aws.Bool(false),
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to query: %w", err))
	}

	items := make([]*models.InKindDisbursement, 0, len(result.Items))
	for _, item := range result.Items {
		var d models.InKindDisbursement
		if err := attributevalue.UnmarshalMap(item, &d); err != nil {
			op.Logger().WithError(err).Warn("failed to unmarshal in-kind disbursement; skipping")
			continue
		}
		items = append(items, &d)
	}
	op.With("count", len(items))
	return items, nil
}
