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

type DisbursementRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
	idGen     *ids.Generator
}

func NewDisbursementRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *DisbursementRepository {
	return &DisbursementRepository{client: client, tableName: tableName, logger: logger, idGen: ids.NewGenerator(client, tableName)}
}

// Create records a new offline disbursement.
func (r *DisbursementRepository) Create(ctx context.Context, d *models.Disbursement) error {
	op := logging.Start(ctx, r.logger, "Disbursement.Create", logrus.Fields{
		"de_id": d.DEID, "amount": d.AmountZMW,
	})
	defer op.End()

	if d.DisbursedAt == "" {
		d.DisbursedAt = time.Now().UTC().Format(time.RFC3339)
	}

	if d.DisbursementID == "" {
		id, err := r.idGen.NextID(ctx, ids.Disbursement)
		if err != nil {
			return op.Fail(fmt.Errorf("failed to generate disbursement_id: %w", err))
		}
		d.DisbursementID = id
	}

	item, err := attributevalue.MarshalMap(d)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal disbursement: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: d.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: d.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to create disbursement: %w", err))
	}
	return nil
}

// ListByDE returns all disbursements for a DE sorted by disbursed_at descending.
func (r *DisbursementRepository) ListByDE(ctx context.Context, deID string) ([]*models.Disbursement, error) {
	op := logging.Start(ctx, r.logger, "Disbursement.ListByDE", logrus.Fields{"de_id": deID})
	defer op.End()

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		KeyConditionExpression: aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: "DISBURSEMENT!" + deID},
		},
		ScanIndexForward: aws.Bool(false),
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to list disbursements: %w", err))
	}

	var items []*models.Disbursement
	for _, item := range result.Items {
		var d models.Disbursement
		if err := attributevalue.UnmarshalMap(item, &d); err != nil {
			continue
		}
		items = append(items, &d)
	}

	op.With("count", len(items))
	return items, nil
}

// GetLatest returns the most recent disbursement for a DE, or nil if none exists.
func (r *DisbursementRepository) GetLatest(ctx context.Context, deID string) (*models.Disbursement, error) {
	op := logging.Start(ctx, r.logger, "Disbursement.GetLatest", logrus.Fields{"de_id": deID})
	defer op.End()

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		KeyConditionExpression: aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: "DISBURSEMENT!" + deID},
		},
		ScanIndexForward: aws.Bool(false),
		Limit:            aws.Int32(1),
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get latest disbursement: %w", err))
	}
	if len(result.Items) == 0 {
		return nil, nil
	}

	var d models.Disbursement
	if err := attributevalue.UnmarshalMap(result.Items[0], &d); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal disbursement: %w", err))
	}
	return &d, nil
}
