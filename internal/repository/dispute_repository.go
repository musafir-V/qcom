// internal/repository/dispute_repository.go
package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

// ErrDisputeAlreadyOpen is returned when an open dispute already exists for the order.
var ErrDisputeAlreadyOpen = errors.New("an open dispute already exists for this order")

type DisputeRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewDisputeRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *DisputeRepository {
	return &DisputeRepository{client: client, tableName: tableName, logger: logger}
}

func buildOpenGuardKey(orderID string) (string, string) {
	return models.DisputeOpenGuardPK(orderID), "METADATA"
}

// Create writes the dispute item and an open-guard item atomically. The guard's
// attribute_not_exists(PK) condition enforces one open dispute per order.
func (r *DisputeRepository) Create(ctx context.Context, d *models.Dispute) error {
	d.DisputeOrderID = d.OrderID

	item, err := attributevalue.MarshalMap(d)
	if err != nil {
		return fmt.Errorf("failed to marshal dispute: %w", err)
	}
	item["PK"] = &types.AttributeValueMemberS{Value: d.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: d.GetSK()}

	guardPK, guardSK := buildOpenGuardKey(d.OrderID)

	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				Put: &types.Put{
					TableName: aws.String(r.tableName),
					Item:      item,
				},
			},
			{
				Put: &types.Put{
					TableName:           aws.String(r.tableName),
					ConditionExpression: aws.String("attribute_not_exists(PK)"),
					Item: map[string]types.AttributeValue{
						"PK":         &types.AttributeValueMemberS{Value: guardPK},
						"SK":         &types.AttributeValueMemberS{Value: guardSK},
						"dispute_id": &types.AttributeValueMemberS{Value: d.DisputeID},
						"created_at": &types.AttributeValueMemberS{Value: d.CreatedAt},
					},
				},
			},
		},
	})
	if err != nil {
		var txErr *types.TransactionCanceledException
		if errors.As(err, &txErr) {
			return ErrDisputeAlreadyOpen
		}
		return fmt.Errorf("failed to create dispute: %w", err)
	}
	return nil
}

func (r *DisputeRepository) GetByID(ctx context.Context, id string) (*models.Dispute, error) {
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DISPUTE!" + id},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get dispute %q: %w", id, err)
	}
	if out.Item == nil {
		return nil, nil
	}
	var d models.Dispute
	if err := attributevalue.UnmarshalMap(out.Item, &d); err != nil {
		return nil, fmt.Errorf("failed to unmarshal dispute: %w", err)
	}
	return &d, nil
}

// GetLatestByOrderID returns the most recent dispute for an order via the sparse
// DisputeOrderIndex GSI, or (nil, nil) if none exist.
func (r *DisputeRepository) GetLatestByOrderID(ctx context.Context, orderID string) (*models.Dispute, error) {
	out, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		IndexName:              aws.String("DisputeOrderIndex"),
		KeyConditionExpression: aws.String("dispute_order_id = :oid"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":oid": &types.AttributeValueMemberS{Value: orderID},
		},
		ScanIndexForward: aws.Bool(false), // newest first
		Limit:            aws.Int32(1),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query DisputeOrderIndex: %w", err)
	}
	if len(out.Items) == 0 {
		return nil, nil
	}
	var d models.Dispute
	if err := attributevalue.UnmarshalMap(out.Items[0], &d); err != nil {
		return nil, fmt.Errorf("failed to unmarshal dispute: %w", err)
	}
	return &d, nil
}
