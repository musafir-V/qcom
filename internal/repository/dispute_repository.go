// internal/repository/dispute_repository.go
package repository

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/ids"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

// ErrDisputeAlreadyOpen is returned when an open dispute already exists for the order.
var ErrDisputeAlreadyOpen = errors.New("an open dispute already exists for this order")

type DisputeRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
	idGen     *ids.Generator
}

func NewDisputeRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *DisputeRepository {
	return &DisputeRepository{client: client, tableName: tableName, logger: logger, idGen: ids.NewGenerator(client, tableName)}
}

func buildOpenGuardKey(orderNumber string) (string, string) {
	return models.DisputeOpenGuardPK(orderNumber), "METADATA"
}

// Create writes the dispute item and an open-guard item atomically. The guard's
// attribute_not_exists(PK) condition enforces at most one dispute per order in v1;
// the guard is never cleared until admin tooling lands, so this is effectively a
// permanent per-order cap until an admin manually removes the guard item.
func (r *DisputeRepository) Create(ctx context.Context, d *models.Dispute) error {
	if d.DisputeID == "" {
		id, err := r.idGen.NextID(ctx, ids.Dispute)
		if err != nil {
			return fmt.Errorf("failed to generate dispute_id: %w", err)
		}
		d.DisputeID = id
	}

	d.DisputeOrderNumber = d.OrderNumber
	d.DisputeStatusKey = string(d.Status)
	d.DisputeStoreStatusKey = models.DisputeStoreStatusKeyFor(d.StoreID, d.Status)

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
	cursor, err := encodeStringKeyCursor(base64.StdEncoding, key, false)
	if err != nil {
		return ""
	}
	return cursor
}

func decodeDisputeCursor(cursor string) (map[string]types.AttributeValue, error) {
	key, err := decodeStringKeyCursor(base64.StdEncoding, cursor)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor: %w", err)
	}
	return key, nil
}

// disputeStatusIndexFor picks the index backing a dispute list/count. An empty
// storeID means "all stores" and uses the status-only index; a storeID uses the
// composite index. keyValue is the prefix the caller appends the status to.
func disputeStatusIndexFor(storeID string) (indexName, keyAttr, keyValue string) {
	if storeID == "" {
		return "DisputeStatusIndex", "dispute_status_key", ""
	}
	return "DisputeStoreStatusIndex", "dispute_store_status_key", storeID + "#"
}

// ListByStatus returns a newest-first page of disputes in the given status.
// An empty storeID lists across all stores via DisputeStatusIndex; a storeID
// scopes to that store via DisputeStoreStatusIndex.
func (r *DisputeRepository) ListByStatus(ctx context.Context, status models.DisputeStatus, storeID, cursor string, limit int32) ([]models.Dispute, string, error) {
	start, err := decodeDisputeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	indexName, keyAttr, keyPrefix := disputeStatusIndexFor(storeID)
	out, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		IndexName:              aws.String(indexName),
		KeyConditionExpression: aws.String(keyAttr + " = :s"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":s": &types.AttributeValueMemberS{Value: keyPrefix + string(status)},
		},
		ScanIndexForward:  aws.Bool(false), // newest first
		Limit:             aws.Int32(limit),
		ExclusiveStartKey: start,
	})
	if err != nil {
		return nil, "", fmt.Errorf("failed to query %s: %w", indexName, err)
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
// An empty storeID counts across all stores.
func (r *DisputeRepository) CountByStatus(ctx context.Context, status models.DisputeStatus, storeID string) (int, error) {
	indexName, keyAttr, keyPrefix := disputeStatusIndexFor(storeID)
	var total int
	var start map[string]types.AttributeValue
	for {
		out, err := r.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(r.tableName),
			IndexName:              aws.String(indexName),
			KeyConditionExpression: aws.String(keyAttr + " = :s"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":s": &types.AttributeValueMemberS{Value: keyPrefix + string(status)},
			},
			Select:            types.SelectCount,
			ExclusiveStartKey: start,
		})
		if err != nil {
			return 0, fmt.Errorf("failed to count %s: %w", indexName, err)
		}
		total += int(out.Count)
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		start = out.LastEvaluatedKey
	}
	return total, nil
}

// UpdateStatus mutates status/note/actor/timestamp and keeps both index keys in
// sync. storeID is the dispute's own store; when empty (a dispute created before
// store attribution) dispute_store_status_key is left absent so the row stays out
// of the sparse composite index rather than landing under a bogus key.
// Returns (nil, nil) if no dispute with that id exists.
func (r *DisputeRepository) UpdateStatus(ctx context.Context, id string, newStatus models.DisputeStatus, note, actor, now, storeID string) (*models.Dispute, error) {
	setExpr := "#status = :status, dispute_status_key = :statuskey, updated_at = :now, resolved_by = :actor"
	values := map[string]types.AttributeValue{
		":status":    &types.AttributeValueMemberS{Value: string(newStatus)},
		":statuskey": &types.AttributeValueMemberS{Value: string(newStatus)},
		":now":       &types.AttributeValueMemberS{Value: now},
		":actor":     &types.AttributeValueMemberS{Value: actor},
	}
	if storeKey := models.DisputeStoreStatusKeyFor(storeID, newStatus); storeKey != "" {
		setExpr += ", dispute_store_status_key = :storekey"
		values[":storekey"] = &types.AttributeValueMemberS{Value: storeKey}
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
