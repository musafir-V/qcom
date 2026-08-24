package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/ids"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/money"
	"github.com/sirupsen/logrus"
)

// encodeCursor serializes a DynamoDB LastEvaluatedKey into an opaque, URL-safe
// pagination token. The AssignedStoreIndex keys (table PK/SK + GSI hash/sort)
// are all string attributes, so we flatten to a string map before encoding.
// Returns "" for an empty/absent key (i.e. the last page).
func encodeCursor(lastKey map[string]types.AttributeValue) (string, error) {
	if len(lastKey) == 0 {
		return "", nil
	}
	flat := make(map[string]string, len(lastKey))
	for k, v := range lastKey {
		s, ok := v.(*types.AttributeValueMemberS)
		if !ok {
			return "", fmt.Errorf("unexpected non-string key attribute %q in cursor", k)
		}
		flat[k] = s.Value
	}
	raw, err := json.Marshal(flat)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(raw), nil
}

// decodeCursor reverses encodeCursor. An empty token yields a nil start key
// (first page).
func decodeCursor(cursor string) (map[string]types.AttributeValue, error) {
	if strings.TrimSpace(cursor) == "" {
		return nil, nil
	}
	raw, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, err
	}
	var flat map[string]string
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil, err
	}
	key := make(map[string]types.AttributeValue, len(flat))
	for k, v := range flat {
		key[k] = &types.AttributeValueMemberS{Value: v}
	}
	return key, nil
}

// ErrCashDepositConflict is returned when a cash-deposit transaction is
// cancelled — either the in-hand balance changed since it was read, or the
// deposit_id was already applied (idempotent replay). Callers map this to 409.
var ErrCashDepositConflict = errors.New("cash deposit conflict: balance changed or deposit_id already applied")

// ErrScanDeadlineConflict is returned by MarkOfflineIfDeadlinePassed when the
// conditional offline write is rejected — the DE is no longer eligible/free or
// re-scanned (deadline changed) since the sweep read it. Callers treat this as
// a benign no-op.
var ErrScanDeadlineConflict = errors.New("scan deadline conflict: DE re-scanned or no longer on duty")

type DERepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
	idGen     *ids.Generator
}

func NewDERepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *DERepository {
	return &DERepository{client: client, tableName: tableName, logger: logger, idGen: ids.NewGenerator(client, tableName)}
}

func (r *DERepository) Create(ctx context.Context, de *models.DeliveryExecutive) error {
	op := logging.Start(ctx, r.logger, "Create", logrus.Fields{"phone": de.PhoneNumber})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)
	de.CreatedAt = now
	de.UpdatedAt = now
	de.Status = models.DEStatusOffline

	// Keep the AssignedStoreIndex attributes consistent with the assigned store
	// and name so the DE is queryable by store (or the Unassigned bucket) and by
	// name prefix from the moment it is created.
	de.AssignedStoreIndexKey = models.AssignedStoreIndexKeyFor(de.AssignedStoreID)
	de.NameLower = models.NameLower(de.Name)

	if de.DEID == "" {
		id, err := r.idGen.NextID(ctx, ids.DE)
		if err != nil {
			return op.Fail(fmt.Errorf("failed to generate de_id: %w", err))
		}
		de.DEID = id
	}

	item, err := attributevalue.MarshalMap(de)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal DE: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: de.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: de.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(r.tableName),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return op.Outcome("already_exists", fmt.Errorf("delivery executive already registered with this number"))
		}
		return op.Fail(fmt.Errorf("failed to create DE: %w", err))
	}
	return nil
}

func (r *DERepository) GetByPhone(ctx context.Context, phone string) (*models.DeliveryExecutive, error) {
	op := logging.Start(ctx, r.logger, "GetByPhone", logrus.Fields{"phone": phone})
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get DE: %w", err))
	}
	if result.Item == nil {
		op.With("found", false)
		return nil, nil
	}

	var de models.DeliveryExecutive
	if err := attributevalue.UnmarshalMap(result.Item, &de); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal DE: %w", err))
	}
	de.PhoneNumber = phone

	op.With("found", true)
	return &de, nil
}

func (r *DERepository) Exists(ctx context.Context, phone string) (bool, error) {
	op := logging.Start(ctx, r.logger, "Exists", logrus.Fields{"phone": phone})
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		ProjectionExpression: aws.String("PK"),
	})
	if err != nil {
		return false, op.Fail(fmt.Errorf("failed to check DE existence: %w", err))
	}

	found := result.Item != nil
	op.With("found", found)
	return found, nil
}

// ErrDENotFound is returned when an update targets a DE that does not exist.
var ErrDENotFound = errors.New("delivery executive not found")

// UpdateAssignedStore sets (or clears) the DE's permanent home darkstore and
// keeps the AssignedStoreIndex hash key in sync. Pass an empty assignedStoreID
// to unassign (the index key falls back to the UNASSIGNED sentinel). This does
// NOT touch current_store_id / duty_index_key / status — an on-duty rider stays
// on duty; the new assignment only gates their next duty-start scan. Returns
// ErrDENotFound if the DE does not exist.
func (r *DERepository) UpdateAssignedStore(ctx context.Context, phone, assignedStoreID string) error {
	op := logging.Start(ctx, r.logger, "UpdateAssignedStore", logrus.Fields{
		"phone": phone, "assigned_store_id": assignedStoreID,
	})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)
	indexKey := models.AssignedStoreIndexKeyFor(assignedStoreID)

	setItems := []string{"assigned_store_index_key = :idx", "updated_at = :now"}
	values := map[string]types.AttributeValue{
		":idx": &types.AttributeValueMemberS{Value: indexKey},
		":now": &types.AttributeValueMemberS{Value: now},
	}
	var removeItems []string
	if strings.TrimSpace(assignedStoreID) == "" {
		removeItems = append(removeItems, "assigned_store_id")
	} else {
		setItems = append(setItems, "assigned_store_id = :store")
		values[":store"] = &types.AttributeValueMemberS{Value: assignedStoreID}
	}

	expr := "SET " + strings.Join(setItems, ", ")
	if len(removeItems) > 0 {
		expr += " REMOVE " + strings.Join(removeItems, ", ")
	}

	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression:          aws.String(expr),
		ConditionExpression:       aws.String("attribute_exists(PK)"),
		ExpressionAttributeValues: values,
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return op.Outcome("not_found", ErrDENotFound)
		}
		return op.Fail(fmt.Errorf("failed to update assigned store: %w", err))
	}
	return nil
}

// ListByAssignedStore returns a page of DEs whose permanent home darkstore is
// indexKey (a store ID, or models.UnassignedStoreSentinel), ordered by name
// via the AssignedStoreIndex GSI. namePrefix (already lowercased by the caller)
// applies a begins_with on the name_lower sort key. cursor is an opaque token
// from a previous call; pass "" for the first page. Returns the page and the
// next cursor ("" when exhausted).
func (r *DERepository) ListByAssignedStore(ctx context.Context, indexKey, namePrefix, cursor string, limit int32) ([]*models.DeliveryExecutive, string, error) {
	op := logging.Start(ctx, r.logger, "ListByAssignedStore", logrus.Fields{
		"index_key": indexKey, "name_prefix": namePrefix,
	})
	defer op.End()

	keyCond := "assigned_store_index_key = :idx"
	values := map[string]types.AttributeValue{
		":idx": &types.AttributeValueMemberS{Value: indexKey},
	}
	if namePrefix != "" {
		keyCond += " AND begins_with(name_lower, :prefix)"
		values[":prefix"] = &types.AttributeValueMemberS{Value: namePrefix}
	}

	startKey, err := decodeCursor(cursor)
	if err != nil {
		return nil, "", op.Outcome("bad_cursor", fmt.Errorf("invalid cursor: %w", err))
	}

	input := &dynamodb.QueryInput{
		TableName:                 aws.String(r.tableName),
		IndexName:                 aws.String("AssignedStoreIndex"),
		KeyConditionExpression:    aws.String(keyCond),
		ExpressionAttributeValues: values,
		ScanIndexForward:          aws.Bool(true),
		ExclusiveStartKey:         startKey,
	}
	if limit > 0 {
		input.Limit = aws.Int32(limit)
	}

	result, err := r.client.Query(ctx, input)
	if err != nil {
		return nil, "", op.Fail(fmt.Errorf("failed to query DEs by assigned store: %w", err))
	}

	des := make([]*models.DeliveryExecutive, 0, len(result.Items))
	for _, item := range result.Items {
		var de models.DeliveryExecutive
		if err := attributevalue.UnmarshalMap(item, &de); err != nil {
			op.Logger().WithError(err).Warn("failed to unmarshal DE; skipping")
			continue
		}
		des = append(des, &de)
	}

	nextCursor, err := encodeCursor(result.LastEvaluatedKey)
	if err != nil {
		return nil, "", op.Fail(fmt.Errorf("failed to encode cursor: %w", err))
	}

	op.With("count", len(des))
	return des, nextCursor, nil
}

// UpdateStatus transitions the DE to a new status and updates related fields.
// Pass empty strings for storeID and orderID to clear those fields.
func (r *DERepository) UpdateStatus(ctx context.Context, phone string, status models.DEStatus, storeID, orderID string) error {
	op := logging.Start(ctx, r.logger, "UpdateStatus", logrus.Fields{
		"phone":  phone,
		"status": string(status),
	})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)

	// Build duty_index_key for the DEDutyIndex GSI. On-duty = eligible OR free
	// (both keep the DE queryable by store); busy/offline clear it.
	dutyIndexKey := ""
	if (status == models.DEStatusEligible || status == models.DEStatusFree) && storeID != "" {
		dutyIndexKey = models.DutyIndexKeyOnDuty(storeID)
	}

	names := map[string]string{"#status": "status"}
	values := map[string]types.AttributeValue{
		":status":     &types.AttributeValueMemberS{Value: string(status)},
		":updated_at": &types.AttributeValueMemberS{Value: now},
	}

	// Build SET and REMOVE lists separately to avoid mixing their syntax.
	setItems := []string{"#status = :status", "updated_at = :updated_at"}
	var removeItems []string

	if storeID != "" {
		setItems = append(setItems, "current_store_id = :store_id")
		values[":store_id"] = &types.AttributeValueMemberS{Value: storeID}
	} else {
		removeItems = append(removeItems, "current_store_id")
	}

	if orderID != "" {
		setItems = append(setItems, "current_order_id = :order_id")
		values[":order_id"] = &types.AttributeValueMemberS{Value: orderID}
	} else {
		removeItems = append(removeItems, "current_order_id")
	}

	if dutyIndexKey != "" {
		setItems = append(setItems, "duty_index_key = :duty_key")
		values[":duty_key"] = &types.AttributeValueMemberS{Value: dutyIndexKey}
	} else {
		// Not on-duty (busy/offline): clear the on-duty index and the stale
		// scan deadline so the sweep never considers this DE.
		removeItems = append(removeItems, "duty_index_key", "scan_deadline_at")
	}

	expr := "SET " + strings.Join(setItems, ", ")
	if len(removeItems) > 0 {
		expr += " REMOVE " + strings.Join(removeItems, ", ")
	}

	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression:          aws.String(expr),
		ExpressionAttributeNames:  names,
		ExpressionAttributeValues: values,
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to update DE status: %w", err))
	}
	return nil
}

// AttachToTrip marks the DE busy on the given trip. Stub for interface
// satisfaction until the force-deliver path implements the Dynamo write.
func (r *DERepository) AttachToTrip(_ context.Context, _, _, _, _ string) error {
	return fmt.Errorf("AttachToTrip: not implemented")
}

// GetByReferralCode looks up a DE using the ReferralCodeIndex GSI.
// The GSI must be configured in DynamoDB: index name "ReferralCodeIndex",
// partition key "referral_code", projecting all attributes.
func (r *DERepository) GetByReferralCode(ctx context.Context, code string) (*models.DeliveryExecutive, error) {
	op := logging.Start(ctx, r.logger, "GetByReferralCode", logrus.Fields{"code": code})
	defer op.End()

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		IndexName:              aws.String("ReferralCodeIndex"),
		KeyConditionExpression: aws.String("referral_code = :code"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":code": &types.AttributeValueMemberS{Value: code},
		},
		Limit: aws.Int32(1),
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to query by referral code: %w", err))
	}
	if len(result.Items) == 0 {
		op.With("found", false)
		return nil, nil
	}

	var de models.DeliveryExecutive
	if err := attributevalue.UnmarshalMap(result.Items[0], &de); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal DE: %w", err))
	}
	op.With("found", true)
	return &de, nil
}

// FindEligibleByStore returns all DEs with status=eligible at the given store.
// Uses a table scan with filter — prefer FindEligibleByStoreFIFO (GSI) at scale.
func (r *DERepository) FindEligibleByStore(ctx context.Context, storeID string) ([]*models.DeliveryExecutive, error) {
	op := logging.Start(ctx, r.logger, "FindEligibleByStore", logrus.Fields{"store_id": storeID})
	defer op.End()

	result, err := r.client.Scan(ctx, &dynamodb.ScanInput{
		TableName:                aws.String(r.tableName),
		FilterExpression:         aws.String("duty_index_key = :duty_key AND #status = :eligible"),
		ExpressionAttributeNames: map[string]string{"#status": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":duty_key": &types.AttributeValueMemberS{Value: models.DutyIndexKeyOnDuty(storeID)},
			":eligible": &types.AttributeValueMemberS{Value: string(models.DEStatusEligible)},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to find eligible DEs: %w", err))
	}

	var des []*models.DeliveryExecutive
	for _, item := range result.Items {
		var de models.DeliveryExecutive
		if err := attributevalue.UnmarshalMap(item, &de); err != nil {
			op.Logger().WithError(err).Warn("failed to unmarshal an eligible DE; skipping")
			continue
		}
		des = append(des, &de)
	}

	op.With("count", len(des))
	return des, nil
}

// FindEligibleByStoreFIFO returns eligible DEs for a store sorted by updated_at
// ascending (FIFO). It queries the DEDutyIndex GSI on the shared on-duty
// partition (DE_ONDUTY#{store}) and filters status=eligible, so free riders
// (also on-duty) are visible to the sweep but skipped for assignment.
//
// NOTE: this filter requires the DEDutyIndex GSI to project the `status`
// attribute. If status is not projected the filter drops every item; the safe
// deploy order is to project status before repointing (see docs).
func (r *DERepository) FindEligibleByStoreFIFO(ctx context.Context, storeID string) ([]*models.DeliveryExecutive, error) {
	op := logging.Start(ctx, r.logger, "FindEligibleByStoreFIFO", logrus.Fields{"store_id": storeID})
	defer op.End()

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                aws.String(r.tableName),
		IndexName:                aws.String("DEDutyIndex"),
		KeyConditionExpression:   aws.String("duty_index_key = :duty_key"),
		FilterExpression:         aws.String("#status = :eligible"),
		ExpressionAttributeNames: map[string]string{"#status": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":duty_key": &types.AttributeValueMemberS{Value: models.DutyIndexKeyOnDuty(storeID)},
			":eligible": &types.AttributeValueMemberS{Value: string(models.DEStatusEligible)},
		},
		ScanIndexForward: aws.Bool(true), // ascending updated_at = FIFO
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to find eligible DEs: %w", err))
	}

	var des []*models.DeliveryExecutive
	for _, item := range result.Items {
		var de models.DeliveryExecutive
		if err := attributevalue.UnmarshalMap(item, &de); err != nil {
			op.Logger().WithError(err).Warn("failed to unmarshal DE; skipping")
			continue
		}
		des = append(des, &de)
	}

	op.With("count", len(des))
	return des, nil
}

// FindOnDutyByStore returns every on-duty DE at a store — status eligible OR
// free — via the DEDutyIndex GSI on DE_ONDUTY#{store}. The sweep uses this to
// find riders whose scan deadline has passed. busy/offline DEs are absent
// because they clear duty_index_key.
func (r *DERepository) FindOnDutyByStore(ctx context.Context, storeID string) ([]*models.DeliveryExecutive, error) {
	op := logging.Start(ctx, r.logger, "FindOnDutyByStore", logrus.Fields{"store_id": storeID})
	defer op.End()

	var des []*models.DeliveryExecutive
	var lastKey map[string]types.AttributeValue
	for {
		result, err := r.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(r.tableName),
			IndexName:              aws.String("DEDutyIndex"),
			KeyConditionExpression: aws.String("duty_index_key = :duty_key"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":duty_key": &types.AttributeValueMemberS{Value: models.DutyIndexKeyOnDuty(storeID)},
			},
			ScanIndexForward:  aws.Bool(true),
			ExclusiveStartKey: lastKey,
		})
		if err != nil {
			return nil, op.Fail(fmt.Errorf("failed to find on-duty DEs: %w", err))
		}

		for _, item := range result.Items {
			var de models.DeliveryExecutive
			if err := attributevalue.UnmarshalMap(item, &de); err != nil {
				op.Logger().WithError(err).Warn("failed to unmarshal on-duty DE; skipping")
				continue
			}
			des = append(des, &de)
		}

		if len(result.LastEvaluatedKey) == 0 {
			break
		}
		lastKey = result.LastEvaluatedKey
	}

	op.With("count", len(des))
	return des, nil
}

// MarkEligibleFromScan transitions a DE to eligible after a validated presence
// scan: it stamps the store, the new scan deadline, and the last-scan location,
// and sets duty_index_key = DE_ONDUTY#{store}. current_order_id is cleared.
func (r *DERepository) MarkEligibleFromScan(ctx context.Context, phone, storeID, deadline string, lat, lng float64, scanAt string) error {
	op := logging.Start(ctx, r.logger, "MarkEligibleFromScan", logrus.Fields{
		"phone": phone, "store_id": storeID,
	})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression: aws.String(
			"SET #status = :eligible, current_store_id = :store, duty_index_key = :duty, " +
				"scan_deadline_at = :deadline, last_scan_lat = :lat, last_scan_lng = :lng, " +
				"last_scan_at = :scan_at, updated_at = :now REMOVE current_order_id",
		),
		ExpressionAttributeNames: map[string]string{"#status": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":eligible": &types.AttributeValueMemberS{Value: string(models.DEStatusEligible)},
			":store":    &types.AttributeValueMemberS{Value: storeID},
			":duty":     &types.AttributeValueMemberS{Value: models.DutyIndexKeyOnDuty(storeID)},
			":deadline": &types.AttributeValueMemberS{Value: deadline},
			":lat":      &types.AttributeValueMemberN{Value: strconv.FormatFloat(lat, 'f', -1, 64)},
			":lng":      &types.AttributeValueMemberN{Value: strconv.FormatFloat(lng, 'f', -1, 64)},
			":scan_at":  &types.AttributeValueMemberS{Value: scanAt},
			":now":      &types.AttributeValueMemberS{Value: now},
		},
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to mark DE eligible from scan: %w", err))
	}
	return nil
}

// MarkOfflineIfDeadlinePassed flips an on-duty DE offline, clearing the
// duty_index_key and scan deadline. It is conditional: the DE must still be
// eligible or free AND its scan_deadline_at must equal expectedDeadline, so a
// rider who re-scanned (new deadline) between the sweep's read and write is not
// wrongly offlined. Returns ErrScanDeadlineConflict when the guard fails.
func (r *DERepository) MarkOfflineIfDeadlinePassed(ctx context.Context, phone, expectedDeadline string) error {
	op := logging.Start(ctx, r.logger, "MarkOfflineIfDeadlinePassed", logrus.Fields{"phone": phone})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression:         aws.String("SET #status = :offline, updated_at = :now REMOVE duty_index_key, scan_deadline_at"),
		ConditionExpression:      aws.String("(#status = :eligible OR #status = :free) AND scan_deadline_at = :expected"),
		ExpressionAttributeNames: map[string]string{"#status": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":offline":  &types.AttributeValueMemberS{Value: string(models.DEStatusOffline)},
			":eligible": &types.AttributeValueMemberS{Value: string(models.DEStatusEligible)},
			":free":     &types.AttributeValueMemberS{Value: string(models.DEStatusFree)},
			":expected": &types.AttributeValueMemberS{Value: expectedDeadline},
			":now":      &types.AttributeValueMemberS{Value: now},
		},
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return op.Outcome("conflict", ErrScanDeadlineConflict)
		}
		return op.Fail(fmt.Errorf("failed to mark DE offline: %w", err))
	}
	return nil
}

// ScanAll returns every DE metadata item in the table.
// Uses a paginated table scan filtered to DE metadata items (PK begins with "DE!", SK = "METADATA").
func (r *DERepository) ScanAll(ctx context.Context) ([]*models.DeliveryExecutive, error) {
	op := logging.Start(ctx, r.logger, "ScanAll", nil)
	defer op.End()

	var des []*models.DeliveryExecutive
	var lastKey map[string]types.AttributeValue
	for {
		result, err := r.client.Scan(ctx, &dynamodb.ScanInput{
			TableName:        aws.String(r.tableName),
			FilterExpression: aws.String("begins_with(PK, :prefix) AND SK = :meta"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":prefix": &types.AttributeValueMemberS{Value: "DE!"},
				":meta":   &types.AttributeValueMemberS{Value: "METADATA"},
			},
			ExclusiveStartKey: lastKey,
		})
		if err != nil {
			return nil, op.Fail(fmt.Errorf("failed to scan DEs: %w", err))
		}

		for _, item := range result.Items {
			var de models.DeliveryExecutive
			if err := attributevalue.UnmarshalMap(item, &de); err != nil {
				op.Logger().WithError(err).Warn("failed to unmarshal a DE; skipping")
				continue
			}
			des = append(des, &de)
		}

		if len(result.LastEvaluatedKey) == 0 {
			break
		}
		lastKey = result.LastEvaluatedKey
	}

	op.With("count", len(des))
	return des, nil
}

// IncrementDailyCount atomically increments the DE's daily trip count,
// resetting it to 1 if the stored date differs from today (Zambia timezone).
// Also increments TotalTripsCompleted unconditionally.
// Returns the new daily count after increment.
func (r *DERepository) IncrementDailyCount(ctx context.Context, phone, todayZambia string) (int, error) {
	op := logging.Start(ctx, r.logger, "IncrementDailyCount", logrus.Fields{"phone": phone})
	defer op.End()

	// First fetch current state
	de, err := r.GetByPhone(ctx, phone)
	if err != nil || de == nil {
		return 0, op.Fail(fmt.Errorf("failed to fetch DE for daily count: %w", err))
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var newCount int
	var expr string
	var values map[string]types.AttributeValue

	if de.DailyCountDate != todayZambia {
		// New day — reset to 1
		newCount = 1
		expr = "SET daily_trip_count = :one, daily_count_date = :today, total_trips_completed = if_not_exists(total_trips_completed, :zero) + :one, updated_at = :now"
		values = map[string]types.AttributeValue{
			":one":   &types.AttributeValueMemberN{Value: "1"},
			":zero":  &types.AttributeValueMemberN{Value: "0"},
			":today": &types.AttributeValueMemberS{Value: todayZambia},
			":now":   &types.AttributeValueMemberS{Value: now},
		}
	} else {
		// Same day — increment
		newCount = de.DailyTripCount + 1
		expr = "SET daily_trip_count = daily_trip_count + :one, total_trips_completed = if_not_exists(total_trips_completed, :zero) + :one, updated_at = :now"
		values = map[string]types.AttributeValue{
			":one":  &types.AttributeValueMemberN{Value: "1"},
			":zero": &types.AttributeValueMemberN{Value: "0"},
			":now":  &types.AttributeValueMemberS{Value: now},
		}
	}

	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression:          aws.String(expr),
		ExpressionAttributeValues: values,
	})
	if err != nil {
		return 0, op.Fail(fmt.Errorf("failed to increment daily count: %w", err))
	}

	return newCount, nil
}

// UpdateLastDisbursedAt stamps the last_disbursed_at field on the DE record.
// Called when ops records a disbursement so future earnings summaries reset
// their outstanding-balance window.
func (r *DERepository) UpdateLastDisbursedAt(ctx context.Context, phone, disbursedAt string) error {
	op := logging.Start(ctx, r.logger, "UpdateLastDisbursedAt", logrus.Fields{"phone": phone})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression: aws.String("SET last_disbursed_at = :dat, updated_at = :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":dat": &types.AttributeValueMemberS{Value: disbursedAt},
			":now": &types.AttributeValueMemberS{Value: now},
		},
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to update last_disbursed_at: %w", err))
	}
	return nil
}

// ApplyCashDeposit atomically decrements the DE's in-hand cash to newBalance
// and appends a cash-deposit ledger entry. The DE update is guarded by an
// optimistic condition (in-hand still near the value read) and the ledger Put
// is conditional on the deposit_id not already existing (idempotent retry).
//
// The lock uses a ±CashMatchEpsilonZMW band rather than exact equality so
// float64/DynamoDB binary noise (e.g. 508.9400000000000182 vs 508.94) cannot
// 409 a legitimate deposit. A real concurrent COD accrual (≥ 0.01 ZMW) still
// falls outside the band and conflicts. The write always SETs a 2dp-rounded
// balance, which also cleans any prior pollution.
func (r *DERepository) ApplyCashDeposit(ctx context.Context, phone string, expectedInHand, newBalance float64, entry *models.CashDepositLedger) error {
	op := logging.Start(ctx, r.logger, "ApplyCashDeposit", logrus.Fields{
		"phone": phone, "deposit_id": entry.DepositID,
	})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)

	if entry.DepositID == "" {
		id, err := r.idGen.NextID(ctx, ids.CashDeposit)
		if err != nil {
			return op.Fail(fmt.Errorf("failed to generate deposit_id: %w", err))
		}
		entry.DepositID = id
	}
	op.With("deposit_id", entry.DepositID)

	entry.RequestedAmountZMW = money.Round2ZMW(entry.RequestedAmountZMW)
	entry.AppliedAmountZMW = money.Round2ZMW(entry.AppliedAmountZMW)

	ledgerItem, err := attributevalue.MarshalMap(entry)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal cash deposit entry: %w", err))
	}
	ledgerItem["PK"] = &types.AttributeValueMemberS{Value: entry.GetPK()}
	ledgerItem["SK"] = &types.AttributeValueMemberS{Value: entry.GetSK()}

	expected := money.Round2ZMW(expectedInHand)
	lo := expected - money.CashMatchEpsilonZMW
	hi := expected + money.CashMatchEpsilonZMW
	// Format the epsilon band to 3dp (not FormatZMW) so lo/hi don't collapse
	// back to the same 2dp value after rounding.
	fmtBand := func(f float64) string { return strconv.FormatFloat(f, 'f', 3, 64) }

	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				Update: &types.Update{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: "DE!" + phone},
						"SK": &types.AttributeValueMemberS{Value: "METADATA"},
					},
					UpdateExpression: aws.String("SET in_hand_cash_zmw = :new, updated_at = :now"),
					// DynamoDB forbids if_not_exists() inside a ConditionExpression, so the
					// "attribute absent ⇔ expected 0" case is spelled out explicitly.
					// BETWEEN absorbs float noise around the rounded expected balance.
					ConditionExpression: aws.String(
						"(attribute_not_exists(in_hand_cash_zmw) AND :expected = :zero) OR (in_hand_cash_zmw BETWEEN :lo AND :hi)",
					),
					ExpressionAttributeValues: map[string]types.AttributeValue{
						":new":      &types.AttributeValueMemberN{Value: money.FormatZMW(newBalance)},
						":expected": &types.AttributeValueMemberN{Value: money.FormatZMW(expected)},
						":lo":       &types.AttributeValueMemberN{Value: fmtBand(lo)},
						":hi":       &types.AttributeValueMemberN{Value: fmtBand(hi)},
						":zero":     &types.AttributeValueMemberN{Value: "0.00"},
						":now":      &types.AttributeValueMemberS{Value: now},
					},
				},
			},
			{
				Put: &types.Put{
					TableName:           aws.String(r.tableName),
					Item:                ledgerItem,
					ConditionExpression: aws.String("attribute_not_exists(PK)"),
				},
			},
		},
	})
	if err != nil {
		var txErr *types.TransactionCanceledException
		if errors.As(err, &txErr) {
			return op.Outcome("conflict", ErrCashDepositConflict)
		}
		return op.Fail(fmt.Errorf("failed to apply cash deposit: %w", err))
	}
	return nil
}
