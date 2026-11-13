package repository

import (
	"context"
	"errors"
	"fmt"
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

// ErrMergeConflict is returned when PersistMerge's optimistic lock fails.
var ErrMergeConflict = errors.New("merge conflict: trip or DE changed")

func orderIndexSK(orderID string) string { return "ORDIDX!" + orderID }

func marshalOrderIndexSibling(tripID, orderID string, status models.TripStatus) (map[string]types.AttributeValue, error) {
	item, err := attributevalue.MarshalMap(map[string]interface{}{
		"trip_id":       tripID,
		"trip_order_id": orderID,
		"status":        string(status),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal order index sibling: %w", err)
	}
	item["PK"] = &types.AttributeValueMemberS{Value: "TRIP!" + tripID}
	item["SK"] = &types.AttributeValueMemberS{Value: orderIndexSK(orderID)}
	return item, nil
}

func orderIDsFromTasks(tasks []models.Task) []string {
	seen := map[string]bool{}
	var ids []string
	for _, t := range tasks {
		if t.OrderID == "" || seen[t.OrderID] {
			continue
		}
		seen[t.OrderID] = true
		ids = append(ids, t.OrderID)
	}
	return ids
}

func orderIndexStatusUpdates(tableName, tripID string, orderIDs []string, status models.TripStatus) []types.TransactWriteItem {
	now := time.Now().UTC().Format(time.RFC3339)
	items := make([]types.TransactWriteItem, 0, len(orderIDs))
	for _, oid := range orderIDs {
		if oid == "" {
			continue
		}
		items = append(items, types.TransactWriteItem{
			Update: &types.Update{
				TableName: aws.String(tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: "TRIP!" + tripID},
					"SK": &types.AttributeValueMemberS{Value: orderIndexSK(oid)},
				},
				UpdateExpression: aws.String("SET #status = :status, updated_at = :now"),
				ExpressionAttributeNames: map[string]string{
					"#status": "status",
				},
				ExpressionAttributeValues: map[string]types.AttributeValue{
					":status": &types.AttributeValueMemberS{Value: string(status)},
					":now":    &types.AttributeValueMemberS{Value: now},
				},
			},
		})
	}
	return items
}

func (r *TripRepository) tripsFromOrderIndex(ctx context.Context, orderID string) ([]*models.Trip, error) {
	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		IndexName:              aws.String("OrderIndex"),
		KeyConditionExpression: aws.String("trip_order_id = :oid"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":oid": &types.AttributeValueMemberS{Value: orderID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query OrderIndex: %w", err)
	}
	seen := map[string]bool{}
	var trips []*models.Trip
	for _, item := range result.Items {
		tripID := tripIDFromIndexItem(item)
		if tripID == "" || seen[tripID] {
			continue
		}
		seen[tripID] = true
		full, gerr := r.GetByID(ctx, tripID)
		if gerr != nil {
			return nil, gerr
		}
		if full != nil {
			trips = append(trips, full)
		}
	}
	return trips, nil
}

func tripIDFromIndexItem(item map[string]types.AttributeValue) string {
	if v, ok := item["trip_id"].(*types.AttributeValueMemberS); ok && v.Value != "" {
		return v.Value
	}
	if v, ok := item["PK"].(*types.AttributeValueMemberS); ok {
		const prefix = "TRIP!"
		if len(v.Value) > len(prefix) && v.Value[:len(prefix)] == prefix {
			return v.Value[len(prefix):]
		}
	}
	return ""
}

const (
	distanceFailedGuardSK = "METADATA"
	// maxOrderIndexPages caps a projected OrderIndex walk. A page is ~1MB;
	// 100 pages is far above a real order and stops a runaway LastEvaluatedKey.
	maxOrderIndexPages = 100
)

func distanceFailedGuardPK(orderID string) string { return "DISTFAIL!" + orderID }

func distanceFailedGuardKey(orderID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: distanceFailedGuardPK(orderID)},
		"SK": &types.AttributeValueMemberS{Value: distanceFailedGuardSK},
	}
}

type distanceFailedGuardState int

const (
	distanceFailedGuardAbsent distanceFailedGuardState = iota
	distanceFailedGuardSuppressed
	distanceFailedGuardReleased
)

// distanceFailedGuardState reads the per-order no-route guard.
// The item is not a trip: it has no trip_order_id, so it never appears on OrderIndex.
// Suppressed means "do not create another trip". Released means an operator
// cancelled the distance_failed trip and the next tick may try once.
func (r *TripRepository) distanceFailedGuardState(ctx context.Context, orderID string) (distanceFailedGuardState, error) {
	if orderID == "" {
		return distanceFailedGuardAbsent, nil
	}
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:            aws.String(r.tableName),
		Key:                  distanceFailedGuardKey(orderID),
		ProjectionExpression: aws.String("suppressed"),
		ConsistentRead:       aws.Bool(true),
	})
	if err != nil {
		return distanceFailedGuardAbsent, fmt.Errorf("distance_failed guard: %w", err)
	}
	if len(out.Item) == 0 {
		return distanceFailedGuardAbsent, nil
	}
	if b, ok := out.Item["suppressed"].(*types.AttributeValueMemberBOOL); ok && !b.Value {
		return distanceFailedGuardReleased, nil
	}
	return distanceFailedGuardSuppressed, nil
}

// DistanceCreateBlocked reports whether createTrip must skip Distance Matrix
// for this order. True only when the no-route guard is suppressed.
func (r *TripRepository) DistanceCreateBlocked(ctx context.Context, orderID string) (bool, error) {
	st, err := r.distanceFailedGuardState(ctx, orderID)
	if err != nil {
		return false, err
	}
	return st == distanceFailedGuardSuppressed, nil
}

// assignmentBlocksCreate is the cron's create/no-create decision.
// backfill persists a suppressed guard so the next tick is one GetItem,
// not another OrderIndex walk over historical distance_failed trips.
func assignmentBlocksCreate(st distanceFailedGuardState, hasLive, sawDistanceFailed bool) (blocked, backfill bool) {
	switch {
	case st == distanceFailedGuardSuppressed:
		return true, false
	case hasLive:
		return false, false
	case st == distanceFailedGuardReleased:
		// Explicit retry. Leftover distance_failed rows must not re-block it.
		return false, false
	case sawDistanceFailed:
		return true, true
	default:
		return false, false
	}
}

func distanceFailedGuardSuppress(table, orderID, now string) types.TransactWriteItem {
	return types.TransactWriteItem{
		Update: &types.Update{
			TableName:        aws.String(table),
			Key:              distanceFailedGuardKey(orderID),
			UpdateExpression: aws.String("SET suppressed = :t, order_id = :oid, updated_at = :now"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":t":   &types.AttributeValueMemberBOOL{Value: true},
				":oid": &types.AttributeValueMemberS{Value: orderID},
				":now": &types.AttributeValueMemberS{Value: now},
			},
		},
	}
}

func distanceFailedGuardRelease(table, orderID, now string) types.TransactWriteItem {
	return types.TransactWriteItem{
		Update: &types.Update{
			TableName:        aws.String(table),
			Key:              distanceFailedGuardKey(orderID),
			UpdateExpression: aws.String("SET suppressed = :f, order_id = :oid, updated_at = :now"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":f":   &types.AttributeValueMemberBOOL{Value: false},
				":oid": &types.AttributeValueMemberS{Value: orderID},
				":now": &types.AttributeValueMemberS{Value: now},
			},
		},
	}
}

func (r *TripRepository) suppressDistanceFailedGuard(ctx context.Context, orderID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:        aws.String(r.tableName),
		Key:              distanceFailedGuardKey(orderID),
		UpdateExpression: aws.String("SET suppressed = :t, order_id = :oid, updated_at = :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":t":   &types.AttributeValueMemberBOOL{Value: true},
			":oid": &types.AttributeValueMemberS{Value: orderID},
			":now": &types.AttributeValueMemberS{Value: now},
		},
	})
	if err != nil {
		return fmt.Errorf("suppress distance_failed guard: %w", err)
	}
	return nil
}

func orderIndexStatus(item map[string]types.AttributeValue) string {
	if v, ok := item["status"].(*types.AttributeValueMemberS); ok {
		return v.Value
	}
	return ""
}

// noteOrderIndexStatus classifies one OrderIndex row.
// distance_failed blocks a new create and is not hydrated.
// cancelled, completed, and inactive are ignored.
// Any other status, including a missing one, is hydrated — it may be live.
func noteOrderIndexStatus(status, tripID string, seen map[string]bool) (liveID string, distanceFailed bool) {
	switch models.TripStatus(status) {
	case models.TripStatusDistanceFailed:
		return "", true
	case models.TripStatusCancelled, models.TripStatusCompleted, models.TripStatusInactive:
		return "", false
	default:
		if tripID == "" || seen[tripID] {
			return "", false
		}
		seen[tripID] = true
		return tripID, false
	}
}

// scanOrderIndexStatuses walks OrderIndex for orderID without hydrating
// terminal trips. Live (or unknown) trip IDs are returned for GetByID.
func (r *TripRepository) scanOrderIndexStatuses(ctx context.Context, orderID string) (liveIDs []string, sawDistanceFailed bool, err error) {
	seen := map[string]bool{}
	var start map[string]types.AttributeValue
	for page := 0; page < maxOrderIndexPages; page++ {
		out, err := r.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(r.tableName),
			IndexName:              aws.String("OrderIndex"),
			KeyConditionExpression: aws.String("trip_order_id = :oid"),
			ExpressionAttributeNames: map[string]string{
				"#status": "status",
			},
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":oid": &types.AttributeValueMemberS{Value: orderID},
			},
			ProjectionExpression: aws.String("trip_id, PK, #status"),
			ExclusiveStartKey:    start,
		})
		if err != nil {
			return nil, false, fmt.Errorf("failed to query OrderIndex: %w", err)
		}
		for _, item := range out.Items {
			liveID, failed := noteOrderIndexStatus(orderIndexStatus(item), tripIDFromIndexItem(item), seen)
			if failed {
				sawDistanceFailed = true
			}
			if liveID != "" {
				liveIDs = append(liveIDs, liveID)
			}
		}
		if len(out.LastEvaluatedKey) == 0 {
			return liveIDs, sawDistanceFailed, nil
		}
		start = out.LastEvaluatedKey
	}
	return nil, false, fmt.Errorf("order index for %s exceeded %d pages", orderID, maxOrderIndexPages)
}

func (r *TripRepository) newestLiveTrip(ctx context.Context, orderID string, ids []string) (*models.Trip, error) {
	var live []*models.Trip
	for _, id := range ids {
		full, err := r.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if full != nil && models.IsLiveTripStatus(full.Status) {
			live = append(live, full)
		}
	}
	if len(live) == 0 {
		return nil, nil
	}
	best := live[0]
	for _, t := range live[1:] {
		if t.CreatedAt > best.CreatedAt {
			best = t
		}
	}
	if len(live) > 1 {
		r.logger.WithFields(logrus.Fields{
			"order_id": orderID,
			"count":    len(live),
			"trip_id":  best.TripID,
		}).Error("LiveTripByOrderID: multiple live trips; using newest created_at")
	}
	return best, nil
}

// LiveTripByOrderID returns the unique live trip for this order.
// If several live trips exist, the newest created_at wins and an error is logged.
// A suppressed distance_failed guard short-circuits the OrderIndex walk:
// that order has no live trip, and hydrating its terminal history is what
// made this lookup fan out into one GetItem per junk trip.
func (r *TripRepository) LiveTripByOrderID(ctx context.Context, orderID string) (*models.Trip, error) {
	op := logging.Start(ctx, r.logger, "TripRepository.LiveTripByOrderID", logrus.Fields{"order_id": orderID})
	defer op.End()

	st, err := r.distanceFailedGuardState(ctx, orderID)
	if err != nil {
		return nil, op.Fail(err)
	}
	if st == distanceFailedGuardSuppressed {
		return nil, nil
	}
	ids, _, err := r.scanOrderIndexStatuses(ctx, orderID)
	if err != nil {
		return nil, op.Fail(err)
	}
	trip, err := r.newestLiveTrip(ctx, orderID, ids)
	if err != nil {
		return nil, op.Fail(err)
	}
	return trip, nil
}

// ReleaseDistanceFailedGuard clears a suppressed no-route guard so the
// assignment cron may try the order once. Absent or already-released guards
// are a no-op. Order cancel uses this because live-trip lookup will not
// return a distance_failed trip to cancel by id.
func (r *TripRepository) ReleaseDistanceFailedGuard(ctx context.Context, orderID string) error {
	if orderID == "" {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(r.tableName),
		Key:                 distanceFailedGuardKey(orderID),
		ConditionExpression: aws.String("attribute_exists(PK) AND suppressed = :t"),
		UpdateExpression:    aws.String("SET suppressed = :f, order_id = :oid, updated_at = :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":f":   &types.AttributeValueMemberBOOL{Value: false},
			":t":   &types.AttributeValueMemberBOOL{Value: true},
			":oid": &types.AttributeValueMemberS{Value: orderID},
			":now": &types.AttributeValueMemberS{Value: now},
		},
	})
	if err != nil {
		var cond *types.ConditionalCheckFailedException
		if errors.As(err, &cond) {
			return nil
		}
		return fmt.Errorf("release distance_failed guard: %w", err)
	}
	return nil
}

// AssignmentTripForOrder is the assignment cron's existence check.
// The bool is true when this order must not get another trip: the no-route
// guard is suppressed, or this call just suppressed it after finding a
// distance_failed trip. Live-trip lookup ignores that status, which is why
// a persisted distance_failed trip used to be created again every tick.
func (r *TripRepository) AssignmentTripForOrder(ctx context.Context, orderID string) (*models.Trip, bool, error) {
	op := logging.Start(ctx, r.logger, "TripRepository.AssignmentTripForOrder", logrus.Fields{"order_id": orderID})
	defer op.End()

	st, err := r.distanceFailedGuardState(ctx, orderID)
	if err != nil {
		return nil, false, op.Fail(err)
	}
	if st == distanceFailedGuardSuppressed {
		return nil, true, nil
	}

	ids, sawFailed, err := r.scanOrderIndexStatuses(ctx, orderID)
	if err != nil {
		return nil, false, op.Fail(err)
	}
	live, err := r.newestLiveTrip(ctx, orderID, ids)
	if err != nil {
		return nil, false, op.Fail(err)
	}
	blocked, backfill := assignmentBlocksCreate(st, live != nil, sawFailed)
	if backfill {
		if perr := r.suppressDistanceFailedGuard(ctx, orderID); perr != nil {
			r.logger.WithError(perr).WithField("order_id", orderID).
				Warn("failed to persist distance_failed guard")
		} else {
			r.logger.WithField("order_id", orderID).
				Warn("distance_failed guard set; assignment cron will not retry this order")
		}
	}
	if blocked {
		return nil, true, nil
	}
	return live, false, nil
}

// ResolveTripByOrderID is LiveTripByOrderID, then follows superseded_by_trip_id
// on inactive hits until a live trip or a missing successor.
func (r *TripRepository) ResolveTripByOrderID(ctx context.Context, orderID string) (*models.Trip, error) {
	op := logging.Start(ctx, r.logger, "TripRepository.ResolveTripByOrderID", logrus.Fields{"order_id": orderID})
	defer op.End()

	live, err := r.LiveTripByOrderID(ctx, orderID)
	if err != nil || live != nil {
		return live, err
	}
	trips, err := r.tripsFromOrderIndex(ctx, orderID)
	if err != nil {
		return nil, op.Fail(err)
	}
	var newestInactive *models.Trip
	for _, t := range trips {
		if t.Status != models.TripStatusInactive || t.SupersededByTripID == "" {
			continue
		}
		if newestInactive == nil || t.CreatedAt > newestInactive.CreatedAt {
			newestInactive = t
		}
	}
	if newestInactive == nil {
		return nil, nil
	}
	seen := map[string]bool{newestInactive.TripID: true}
	id := newestInactive.SupersededByTripID
	for id != "" && !seen[id] {
		seen[id] = true
		t, gerr := r.GetByID(ctx, id)
		if gerr != nil {
			return nil, op.Fail(gerr)
		}
		if t == nil {
			return nil, nil
		}
		if models.IsLiveTripStatus(t.Status) || t.Status == models.TripStatusCompleted {
			return t, nil
		}
		if t.Status != models.TripStatusInactive {
			return nil, nil
		}
		id = t.SupersededByTripID
	}
	var completed *models.Trip
	for _, t := range trips {
		if t.Status != models.TripStatusCompleted {
			continue
		}
		if completed == nil || t.CreatedAt > completed.CreatedAt {
			completed = t
		}
	}
	return completed, nil
}

// AssignIDs fills trip_id and empty task_ids the same way Create does.
func (r *TripRepository) AssignIDs(ctx context.Context, trip *models.Trip) error {
	if trip.TripID == "" {
		id, err := r.idGen.NextID(ctx, ids.Trip)
		if err != nil {
			return fmt.Errorf("failed to generate trip_id: %w", err)
		}
		trip.TripID = id
	}
	for i := range trip.Tasks {
		if trip.Tasks[i].TaskID == "" {
			tid, err := r.idGen.NextID(ctx, ids.Task)
			if err != nil {
				return fmt.Errorf("failed to generate task_id: %w", err)
			}
			trip.Tasks[i].TaskID = tid
		}
	}
	return nil
}

// MergePersistInput is one merge/split write.
type MergePersistInput struct {
	NewTrip            *models.Trip
	CloseTrips         []MergeCloseTrip
	Leftover           *models.Trip
	TargetPhone        string
	TargetExpectedTrip string
	LoserPhone         string
	LoserExpectedTrip  string
	LoserStoreID       string
	FreeLoser          bool
	FreeTarget         bool
	TargetStoreID      string
}

// MergeCloseTrip is a live trip that becomes inactive.
type MergeCloseTrip struct {
	TripID            string
	ExpectedUpdatedAt string
	OrderIDs          []string
	SupersededBy      string
}

// PersistMerge writes the successor trip(s), inactivates predecessors, and
// retargets DE current_* pointers in one transaction.
func (r *TripRepository) PersistMerge(ctx context.Context, in MergePersistInput) error {
	op := logging.Start(ctx, r.logger, "TripRepository.PersistMerge", logrus.Fields{
		"new_trip_id": in.NewTrip.TripID,
	})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)
	if in.NewTrip.CreatedAt == "" {
		in.NewTrip.CreatedAt = now
	}
	in.NewTrip.UpdatedAt = now
	in.NewTrip.SyncIndexKeys()

	var transact []types.TransactWriteItem

	putTrip := func(trip *models.Trip) error {
		item, err := attributevalue.MarshalMap(trip)
		if err != nil {
			return fmt.Errorf("marshal trip: %w", err)
		}
		item["PK"] = &types.AttributeValueMemberS{Value: trip.GetPK()}
		item["SK"] = &types.AttributeValueMemberS{Value: trip.GetSK()}
		transact = append(transact, types.TransactWriteItem{
			Put: &types.Put{
				TableName:           aws.String(r.tableName),
				Item:                item,
				ConditionExpression: aws.String("attribute_not_exists(PK)"),
			},
		})
		for _, oid := range trip.LiveOrderIDs() {
			sib, serr := marshalOrderIndexSibling(trip.TripID, oid, trip.Status)
			if serr != nil {
				return serr
			}
			transact = append(transact, types.TransactWriteItem{
				Put: &types.Put{TableName: aws.String(r.tableName), Item: sib},
			})
		}
		return nil
	}

	if err := putTrip(in.NewTrip); err != nil {
		return op.Fail(err)
	}
	if in.Leftover != nil {
		if in.Leftover.CreatedAt == "" {
			in.Leftover.CreatedAt = now
		}
		in.Leftover.UpdatedAt = now
		in.Leftover.SyncIndexKeys()
		if err := putTrip(in.Leftover); err != nil {
			return op.Fail(err)
		}
	}

	for _, c := range in.CloseTrips {
		cond := "#status <> :completed AND #status <> :cancelled AND #status <> :inactive"
		values := map[string]types.AttributeValue{
			":inactive":  &types.AttributeValueMemberS{Value: string(models.TripStatusInactive)},
			":completed": &types.AttributeValueMemberS{Value: string(models.TripStatusCompleted)},
			":cancelled": &types.AttributeValueMemberS{Value: string(models.TripStatusCancelled)},
			":now":       &types.AttributeValueMemberS{Value: now},
			":succ":      &types.AttributeValueMemberS{Value: c.SupersededBy},
		}
		if c.ExpectedUpdatedAt != "" {
			cond += " AND updated_at = :exp"
			values[":exp"] = &types.AttributeValueMemberS{Value: c.ExpectedUpdatedAt}
		}
		transact = append(transact, types.TransactWriteItem{
			Update: &types.Update{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: "TRIP!" + c.TripID},
					"SK": &types.AttributeValueMemberS{Value: "METADATA"},
				},
				UpdateExpression:          aws.String("SET #status = :inactive, superseded_by_trip_id = :succ, updated_at = :now"),
				ConditionExpression:       aws.String(cond),
				ExpressionAttributeNames:  map[string]string{"#status": "status"},
				ExpressionAttributeValues: values,
			},
		})
		transact = append(transact, orderIndexStatusUpdates(r.tableName, c.TripID, c.OrderIDs, models.TripStatusInactive)...)
	}

	if in.FreeTarget {
		deUpdate := &types.Update{
			TableName: aws.String(r.tableName),
			Key: map[string]types.AttributeValue{
				"PK": &types.AttributeValueMemberS{Value: "DE!" + in.TargetPhone},
				"SK": &types.AttributeValueMemberS{Value: "METADATA"},
			},
			ExpressionAttributeNames: map[string]string{"#status": "status"},
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":free": &types.AttributeValueMemberS{Value: string(models.DEStatusFree)},
				":busy": &types.AttributeValueMemberS{Value: string(models.DEStatusBusy)},
				":old":  &types.AttributeValueMemberS{Value: in.TargetExpectedTrip},
				":now":  &types.AttributeValueMemberS{Value: now},
			},
			ConditionExpression: aws.String("#status = :busy AND current_trip_id = :old"),
		}
		if in.TargetStoreID != "" {
			deUpdate.UpdateExpression = aws.String(
				"SET #status = :free, current_store_id = :store, duty_index_key = :duty, updated_at = :now " +
					"REMOVE current_order_id, current_trip_id, scan_deadline_at",
			)
			deUpdate.ExpressionAttributeValues[":store"] = &types.AttributeValueMemberS{Value: in.TargetStoreID}
			deUpdate.ExpressionAttributeValues[":duty"] = &types.AttributeValueMemberS{Value: models.DutyIndexKeyOnDuty(in.TargetStoreID)}
		} else {
			deUpdate.UpdateExpression = aws.String(
				"SET #status = :free, updated_at = :now REMOVE current_order_id, current_trip_id, duty_index_key, scan_deadline_at",
			)
		}
		transact = append(transact, types.TransactWriteItem{Update: deUpdate})
	} else {
		currentOrder := ""
		if t := in.NewTrip.FirstIncompleteTask(); t != nil {
			currentOrder = in.NewTrip.TaskOrderID(t)
		} else {
			currentOrder = in.NewTrip.OrderID
		}
		transact = append(transact, types.TransactWriteItem{
			Update: &types.Update{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: "DE!" + in.TargetPhone},
					"SK": &types.AttributeValueMemberS{Value: "METADATA"},
				},
				UpdateExpression: aws.String(
					"SET current_trip_id = :tid, current_order_id = :oid, #status = :busy, updated_at = :now " +
						"REMOVE duty_index_key, scan_deadline_at",
				),
				ConditionExpression:      aws.String("#status = :busy AND current_trip_id = :old"),
				ExpressionAttributeNames: map[string]string{"#status": "status"},
				ExpressionAttributeValues: map[string]types.AttributeValue{
					":tid":  &types.AttributeValueMemberS{Value: in.NewTrip.TripID},
					":oid":  &types.AttributeValueMemberS{Value: currentOrder},
					":busy": &types.AttributeValueMemberS{Value: string(models.DEStatusBusy)},
					":old":  &types.AttributeValueMemberS{Value: in.TargetExpectedTrip},
					":now":  &types.AttributeValueMemberS{Value: now},
				},
			},
		})
	}

	if in.LoserPhone != "" {
		if in.FreeLoser || in.Leftover == nil {
			deUpdate := &types.Update{
				TableName: aws.String(r.tableName),
				Key: map[string]types.AttributeValue{
					"PK": &types.AttributeValueMemberS{Value: "DE!" + in.LoserPhone},
					"SK": &types.AttributeValueMemberS{Value: "METADATA"},
				},
				ExpressionAttributeNames: map[string]string{"#status": "status"},
				ExpressionAttributeValues: map[string]types.AttributeValue{
					":free": &types.AttributeValueMemberS{Value: string(models.DEStatusFree)},
					":busy": &types.AttributeValueMemberS{Value: string(models.DEStatusBusy)},
					":tid":  &types.AttributeValueMemberS{Value: in.LoserExpectedTrip},
					":now":  &types.AttributeValueMemberS{Value: now},
				},
				ConditionExpression: aws.String("#status = :busy AND current_trip_id = :tid"),
			}
			if in.LoserStoreID != "" {
				deUpdate.UpdateExpression = aws.String(
					"SET #status = :free, current_store_id = :store, duty_index_key = :duty, updated_at = :now " +
						"REMOVE current_order_id, current_trip_id, scan_deadline_at",
				)
				deUpdate.ExpressionAttributeValues[":store"] = &types.AttributeValueMemberS{Value: in.LoserStoreID}
				deUpdate.ExpressionAttributeValues[":duty"] = &types.AttributeValueMemberS{Value: models.DutyIndexKeyOnDuty(in.LoserStoreID)}
			} else {
				deUpdate.UpdateExpression = aws.String(
					"SET #status = :free, updated_at = :now REMOVE current_order_id, current_trip_id, duty_index_key, scan_deadline_at",
				)
			}
			transact = append(transact, types.TransactWriteItem{Update: deUpdate})
		} else {
			loserOrder := in.Leftover.OrderID
			if t := in.Leftover.FirstIncompleteTask(); t != nil {
				loserOrder = in.Leftover.TaskOrderID(t)
			}
			transact = append(transact, types.TransactWriteItem{
				Update: &types.Update{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: "DE!" + in.LoserPhone},
						"SK": &types.AttributeValueMemberS{Value: "METADATA"},
					},
					UpdateExpression: aws.String(
						"SET current_trip_id = :tid, current_order_id = :oid, #status = :busy, updated_at = :now " +
							"REMOVE duty_index_key, scan_deadline_at",
					),
					ConditionExpression:      aws.String("#status = :busy AND current_trip_id = :old"),
					ExpressionAttributeNames: map[string]string{"#status": "status"},
					ExpressionAttributeValues: map[string]types.AttributeValue{
						":tid":  &types.AttributeValueMemberS{Value: in.Leftover.TripID},
						":oid":  &types.AttributeValueMemberS{Value: loserOrder},
						":busy": &types.AttributeValueMemberS{Value: string(models.DEStatusBusy)},
						":old":  &types.AttributeValueMemberS{Value: in.LoserExpectedTrip},
						":now":  &types.AttributeValueMemberS{Value: now},
					},
				},
			})
		}
	}

	_, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: transact})
	if err != nil {
		var txErr *types.TransactionCanceledException
		if errors.As(err, &txErr) {
			return op.Outcome("conflict", ErrMergeConflict)
		}
		return op.Fail(fmt.Errorf("failed to persist merge: %w", err))
	}
	return nil
}

// CompleteDropKeepBusy updates tasks, accrues COD, and keeps the DE busy on this trip.
func (r *TripRepository) CompleteDropKeepBusy(ctx context.Context, tripID, dePhone, currentOrderID string, tasks []models.Task, codAmount float64) error {
	op := logging.Start(ctx, r.logger, "TripRepository.CompleteDropKeepBusy", logrus.Fields{
		"trip_id": tripID, "cod_amount_zmw": codAmount,
	})
	defer op.End()

	tasksAttr, err := attributevalue.Marshal(tasks)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal tasks: %w", err))
	}
	now := time.Now().UTC().Format(time.RFC3339)

	deExpr := "SET updated_at = :now, in_hand_cash_zmw = if_not_exists(in_hand_cash_zmw, :zero) + :cod"
	deValues := map[string]types.AttributeValue{
		":now":  &types.AttributeValueMemberS{Value: now},
		":cod":  &types.AttributeValueMemberN{Value: money.FormatZMW(codAmount)},
		":zero": &types.AttributeValueMemberN{Value: "0.00"},
		":busy": &types.AttributeValueMemberS{Value: string(models.DEStatusBusy)},
		":tid":  &types.AttributeValueMemberS{Value: tripID},
	}
	if currentOrderID != "" {
		deExpr += ", current_order_id = :oid"
		deValues[":oid"] = &types.AttributeValueMemberS{Value: currentOrderID}
	}

	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []types.TransactWriteItem{
			{
				Update: &types.Update{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: "TRIP!" + tripID},
						"SK": &types.AttributeValueMemberS{Value: "METADATA"},
					},
					UpdateExpression: aws.String("SET tasks = :tasks, updated_at = :now"),
					ConditionExpression: aws.String(
						"#status <> :completed AND #status <> :cancelled AND #status <> :inactive",
					),
					ExpressionAttributeNames: map[string]string{"#status": "status"},
					ExpressionAttributeValues: map[string]types.AttributeValue{
						":tasks":     tasksAttr,
						":now":       &types.AttributeValueMemberS{Value: now},
						":completed": &types.AttributeValueMemberS{Value: string(models.TripStatusCompleted)},
						":cancelled": &types.AttributeValueMemberS{Value: string(models.TripStatusCancelled)},
						":inactive":  &types.AttributeValueMemberS{Value: string(models.TripStatusInactive)},
					},
				},
			},
			{
				Update: &types.Update{
					TableName: aws.String(r.tableName),
					Key: map[string]types.AttributeValue{
						"PK": &types.AttributeValueMemberS{Value: "DE!" + dePhone},
						"SK": &types.AttributeValueMemberS{Value: "METADATA"},
					},
					UpdateExpression:          aws.String(deExpr),
					ConditionExpression:       aws.String("#status = :busy AND current_trip_id = :tid"),
					ExpressionAttributeNames:  map[string]string{"#status": "status"},
					ExpressionAttributeValues: deValues,
				},
			},
		},
	})
	if err != nil {
		var txErr *types.TransactionCanceledException
		if errors.As(err, &txErr) {
			return op.Outcome("conflict", fmt.Errorf("drop keep-busy conflict: trip closed or DE not busy on this trip"))
		}
		return op.Fail(fmt.Errorf("failed to complete drop keep-busy: %w", err))
	}
	return nil
}

// SetCurrentOrderID updates the DE's current_order_id while they stay busy.
func (r *TripRepository) SetCurrentOrderID(ctx context.Context, dePhone, orderID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	expr := "SET updated_at = :now"
	values := map[string]types.AttributeValue{":now": &types.AttributeValueMemberS{Value: now}}
	if orderID == "" {
		expr += " REMOVE current_order_id"
	} else {
		expr += ", current_order_id = :oid"
		values[":oid"] = &types.AttributeValueMemberS{Value: orderID}
	}
	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DE!" + dePhone},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression:          aws.String(expr),
		ExpressionAttributeValues: values,
	})
	return err
}
