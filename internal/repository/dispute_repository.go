// internal/repository/dispute_repository.go
package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

func buildOpenGuardKey(orderNumber string) (string, string) {
	return models.DisputeOpenGuardPK(orderNumber), "METADATA"
}

// Create writes the dispute item and an open-guard item atomically. The guard's
// attribute_not_exists(PK) condition enforces at most one dispute per order in v1;
// the guard is never cleared until admin tooling lands, so this is effectively a
// permanent per-order cap until an admin manually removes the guard item.
func (r *DisputeRepository) Create(ctx context.Context, d *models.Dispute) error {
	d.DisputeOrderNumber = d.OrderNumber
	d.DisputeStatusKey = string(d.Status)

	item, err := attributevalue.MarshalMap(d)
	if err != nil {
		return fmt.Errorf("failed to marshal dispute: %w", err)
	}
	item["PK"] = &types.AttributeValueMemberS{Value: d.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: d.GetSK()}

	guardPK, guardSK := buildOpenGuardKey(d.OrderNumber)

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

// GetLatestByOrderNumber returns the most recent dispute for an order via the sparse
// DisputeOrderIndex GSI, or (nil, nil) if none exist.
func (r *DisputeRepository) GetLatestByOrderNumber(ctx context.Context, orderNumber string) (*models.Dispute, error) {
	out, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		IndexName:              aws.String("DisputeOrderIndex"),
		KeyConditionExpression: aws.String("dispute_order_number = :onum"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":onum": &types.AttributeValueMemberS{Value: orderNumber},
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

func encodeDisputeCursor(key map[string]types.AttributeValue) string {
	if len(key) == 0 {
		return ""
	}
	m := make(map[string]string, len(key))
	for k, v := range key {
		if s, ok := v.(*types.AttributeValueMemberS); ok {
			m[k] = s.Value
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

func decodeDisputeCursor(cursor string) (map[string]types.AttributeValue, error) {
	if cursor == "" {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor: %w", err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("invalid cursor: %w", err)
	}
	out := make(map[string]types.AttributeValue, len(m))
	for k, v := range m {
		out[k] = &types.AttributeValueMemberS{Value: v}
	}
	return out, nil
}

// ListByStatus returns a newest-first page of disputes in the given status via DisputeStatusIndex.
func (r *DisputeRepository) ListByStatus(ctx context.Context, status models.DisputeStatus, cursor string, limit int32) ([]models.Dispute, string, error) {
	start, err := decodeDisputeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	out, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		IndexName:              aws.String("DisputeStatusIndex"),
		KeyConditionExpression: aws.String("dispute_status_key = :s"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":s": &types.AttributeValueMemberS{Value: string(status)},
		},
		ScanIndexForward:  aws.Bool(false), // newest first
		Limit:             aws.Int32(limit),
		ExclusiveStartKey: start,
	})
	if err != nil {
		return nil, "", fmt.Errorf("failed to query DisputeStatusIndex: %w", err)
	}
	disputes := make([]models.Dispute, 0, len(out.Items))
	for _, item := range out.Items {
		var d models.Dispute
		if err := attributevalue.UnmarshalMap(item, &d); err != nil {
			r.logger.WithError(err).Warn("failed to unmarshal dispute; skipping")
			continue
		}
		disputes = append(disputes, d)
	}
	return disputes, encodeDisputeCursor(out.LastEvaluatedKey), nil
}

// CountByStatus counts disputes in a status, paginating COUNT pages for correctness.
func (r *DisputeRepository) CountByStatus(ctx context.Context, status models.DisputeStatus) (int, error) {
	var total int
	var start map[string]types.AttributeValue
	for {
		out, err := r.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(r.tableName),
			IndexName:              aws.String("DisputeStatusIndex"),
			KeyConditionExpression: aws.String("dispute_status_key = :s"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":s": &types.AttributeValueMemberS{Value: string(status)},
			},
			Select:            types.SelectCount,
			ExclusiveStartKey: start,
		})
		if err != nil {
			return 0, fmt.Errorf("failed to count DisputeStatusIndex: %w", err)
		}
		total += int(out.Count)
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		start = out.LastEvaluatedKey
	}
	return total, nil
}

// UpdateStatus mutates status/note/actor/timestamp and keeps dispute_status_key in sync.
// Returns (nil, nil) if no dispute with that id exists.
func (r *DisputeRepository) UpdateStatus(ctx context.Context, id string, newStatus models.DisputeStatus, note, actor, now string) (*models.Dispute, error) {
	setExpr := "#status = :status, dispute_status_key = :statuskey, updated_at = :now, resolved_by = :actor"
	values := map[string]types.AttributeValue{
		":status":    &types.AttributeValueMemberS{Value: string(newStatus)},
		":statuskey": &types.AttributeValueMemberS{Value: string(newStatus)},
		":now":       &types.AttributeValueMemberS{Value: now},
		":actor":     &types.AttributeValueMemberS{Value: actor},
	}
	if note != "" {
		setExpr += ", resolution_note = :note"
		values[":note"] = &types.AttributeValueMemberS{Value: note}
	}
	out, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DISPUTE!" + id},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression:          aws.String("SET " + setExpr),
		ConditionExpression:       aws.String("attribute_exists(PK)"),
		ExpressionAttributeNames:  map[string]string{"#status": "status"},
		ExpressionAttributeValues: values,
		ReturnValues:              types.ReturnValueAllNew,
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return nil, nil // not found
		}
		return nil, fmt.Errorf("failed to update dispute %q: %w", id, err)
	}
	var d models.Dispute
	if err := attributevalue.UnmarshalMap(out.Attributes, &d); err != nil {
		return nil, fmt.Errorf("failed to unmarshal updated dispute: %w", err)
	}
	return &d, nil
}
