package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

const (
	// darkstoreKeyPrefix + id / darkstoreMetadataSK form a darkstore item's primary key.
	darkstoreKeyPrefix  = "DARKSTORE!"
	darkstoreMetadataSK = "METADATA"

	// darkstoreIndexPK / darkstoreIndexSK address the single "index" item that holds
	// the set of all darkstore IDs (attribute darkstore_ids, a String Set). Reading it
	// lets us fetch darkstores by primary key instead of scanning the whole table.
	// Note: its PK ("DARKSTORE", no "!") deliberately does NOT match the scan's
	// begins_with("DARKSTORE!") filter, so the index item is never mistaken for a darkstore.
	darkstoreIndexPK = "DARKSTORE"
	darkstoreIndexSK = "INDEX"

	// batchGetMaxKeys is DynamoDB's hard limit on keys per BatchGetItem request.
	batchGetMaxKeys = 100
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
			"PK": &types.AttributeValueMemberS{Value: darkstoreKeyPrefix + darkstoreID},
			"SK": &types.AttributeValueMemberS{Value: darkstoreMetadataSK},
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

// ListActive returns every active darkstore. It reads the darkstore-ID index item
// and fetches those darkstores by primary key (BatchGetItem), avoiding a full-table
// Scan on the hot serviceability path. If the index item is missing, it falls back
// to the legacy Scan so serviceability keeps working even if the index drifts.
func (r *DarkstoreRepository) ListActive(ctx context.Context) ([]models.Darkstore, error) {
	op := logging.Start(ctx, r.logger, "ListActive", nil)
	defer op.End()

	all, err := r.ListAll(ctx)
	if err != nil {
		return nil, op.Fail(err)
	}

	active := make([]models.Darkstore, 0, len(all))
	for _, ds := range all {
		if ds.IsActive {
			active = append(active, ds)
		}
	}
	op.With("count", len(active))
	return active, nil
}

// ListAll returns every darkstore regardless of is_active. Serviceability uses this
// so inactive stores still match their polygon and return store_inactive instead
// of outside_delivery_zone.
func (r *DarkstoreRepository) ListAll(ctx context.Context) ([]models.Darkstore, error) {
	op := logging.Start(ctx, r.logger, "ListAll", nil)
	defer op.End()

	ids, err := r.getDarkstoreIDs(ctx)
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to read darkstore index: %w", err))
	}

	if len(ids) == 0 {
		op.Logger().Warn("darkstore index missing or empty; falling back to table scan")
		darkstores, err := r.listByScan(ctx, false)
		if err != nil {
			return nil, op.Fail(err)
		}
		op.With("count", len(darkstores)).With("source", "scan")
		return darkstores, nil
	}

	darkstores, err := r.batchGet(ctx, ids)
	if err != nil {
		return nil, op.Fail(err)
	}
	op.With("count", len(darkstores)).With("source", "index")
	return darkstores, nil
}

// getDarkstoreIDs reads the index item (PK=DARKSTORE, SK=INDEX) and returns the set
// of darkstore IDs it lists. Returns nil (no error) when the index item is absent.
func (r *DarkstoreRepository) getDarkstoreIDs(ctx context.Context) ([]string, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: darkstoreIndexPK},
			"SK": &types.AttributeValueMemberS{Value: darkstoreIndexSK},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get darkstore index: %w", err)
	}
	if result.Item == nil {
		return nil, nil
	}

	// darkstore_ids is stored as a String Set, but tolerate a List of strings too.
	switch v := result.Item["darkstore_ids"].(type) {
	case *types.AttributeValueMemberSS:
		return v.Value, nil
	case *types.AttributeValueMemberL:
		ids := make([]string, 0, len(v.Value))
		for _, av := range v.Value {
			if s, ok := av.(*types.AttributeValueMemberS); ok {
				ids = append(ids, s.Value)
			}
		}
		return ids, nil
	default:
		return nil, nil
	}
}

// batchGet fetches the given darkstore IDs by primary key. Keys are chunked to
// DynamoDB's 100-per-request limit and UnprocessedKeys are retried until drained.
func (r *DarkstoreRepository) batchGet(ctx context.Context, ids []string) ([]models.Darkstore, error) {
	var fetched []models.Darkstore

	for start := 0; start < len(ids); start += batchGetMaxKeys {
		end := start + batchGetMaxKeys
		if end > len(ids) {
			end = len(ids)
		}

		keys := make([]map[string]types.AttributeValue, 0, end-start)
		for _, id := range ids[start:end] {
			keys = append(keys, map[string]types.AttributeValue{
				"PK": &types.AttributeValueMemberS{Value: darkstoreKeyPrefix + id},
				"SK": &types.AttributeValueMemberS{Value: darkstoreMetadataSK},
			})
		}

		for len(keys) > 0 {
			result, err := r.client.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{
				RequestItems: map[string]types.KeysAndAttributes{
					r.tableName: {Keys: keys},
				},
			})
			if err != nil {
				return nil, fmt.Errorf("failed to batch get darkstores: %w", err)
			}

			var page []models.Darkstore
			if err := attributevalue.UnmarshalListOfMaps(result.Responses[r.tableName], &page); err != nil {
				return nil, fmt.Errorf("failed to unmarshal darkstores: %w", err)
			}
			fetched = append(fetched, page...)

			if un, ok := result.UnprocessedKeys[r.tableName]; ok && len(un.Keys) > 0 {
				keys = un.Keys
			} else {
				break
			}
		}
	}

	return fetched, nil
}

// listByScan is the legacy full-table Scan, kept as a safety fallback for when
// the darkstore index item is missing.
func (r *DarkstoreRepository) listByScan(ctx context.Context, activeOnly bool) ([]models.Darkstore, error) {
	filter := "begins_with(PK, :pk) AND SK = :sk"
	values := map[string]types.AttributeValue{
		":pk": &types.AttributeValueMemberS{Value: darkstoreKeyPrefix},
		":sk": &types.AttributeValueMemberS{Value: darkstoreMetadataSK},
	}
	if activeOnly {
		filter += " AND is_active = :active"
		values[":active"] = &types.AttributeValueMemberBOOL{Value: true}
	}

	var darkstores []models.Darkstore
	var startKey map[string]types.AttributeValue

	for {
		result, err := r.client.Scan(ctx, &dynamodb.ScanInput{
			TableName:                 aws.String(r.tableName),
			FilterExpression:          aws.String(filter),
			ExpressionAttributeValues: values,
			ExclusiveStartKey:         startKey,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to scan darkstores: %w", err)
		}

		var page []models.Darkstore
		if err := attributevalue.UnmarshalListOfMaps(result.Items, &page); err != nil {
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

// listActiveByScan scans the table for active darkstores only.
func (r *DarkstoreRepository) listActiveByScan(ctx context.Context) ([]models.Darkstore, error) {
	return r.listByScan(ctx, true)
}

// CreateDarkstoreInput carries the fields the admin API accepts. Kept as a
// distinct struct (not *models.Darkstore) so the repository owns ID
// generation, timestamps, and the is_active=false default.
type CreateDarkstoreInput struct {
	Name      string
	Latitude  float64
	Longitude float64
	Polygon   []models.PolygonPoint // may be nil/empty — polygon is optional
	OpensAt   string
	ClosesAt  string
}

// Create allocates a new plain zero-padded darkstore ID (via the dedicated
// COUNTER!DARKSTORE counter — see darkstore_id.go), writes the darkstore item,
// and appends the new ID to the DARKSTORE/INDEX item so ListAll/ListActive
// (servicability + assignment cron) pick it up immediately. is_active is
// always false on create; ops flips it on later via a separate mechanism.
func (r *DarkstoreRepository) Create(ctx context.Context, in CreateDarkstoreInput) (*models.Darkstore, error) {
	op := logging.Start(ctx, r.logger, "Create", logrus.Fields{"name": in.Name})
	defer op.End()

	seq, err := nextDarkstoreCounter(ctx, r.client, r.tableName)
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to allocate darkstore id: %w", err))
	}
	id := formatDarkstoreID(seq)

	now := time.Now().UTC().Format(time.RFC3339)
	ds := &models.Darkstore{
		DarkstoreID: id,
		Name:        in.Name,
		Latitude:    in.Latitude,
		Longitude:   in.Longitude,
		Polygon:     in.Polygon,
		IsActive:    false,
		OpensAt:     in.OpensAt,
		ClosesAt:    in.ClosesAt,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	item, err := attributevalue.MarshalMap(ds)
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to marshal darkstore: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: ds.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: ds.GetSK()}

	if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(r.tableName),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
	}); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to create darkstore: %w", err))
	}

	// Index update is required, not best-effort: if it fails, surface the
	// error so the caller can alert/retry, rather than silently returning
	// success with a store that's invisible to ListAll's index path.
	if _, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: darkstoreIndexPK},
			"SK": &types.AttributeValueMemberS{Value: darkstoreIndexSK},
		},
		UpdateExpression: aws.String("ADD darkstore_ids :id SET updated_at = :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":id":  &types.AttributeValueMemberSS{Value: []string{id}},
			":now": &types.AttributeValueMemberS{Value: now},
		},
	}); err != nil {
		return nil, op.Fail(fmt.Errorf("darkstore %s created but failed to update index: %w", id, err))
	}

	op.With("darkstore_id", id)
	return ds, nil
}
