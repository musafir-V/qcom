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

type EarningsLedgerRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewEarningsLedgerRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *EarningsLedgerRepository {
	return &EarningsLedgerRepository{client: client, tableName: tableName, logger: logger}
}

// Append writes a new earning entry to the ledger.
func (r *EarningsLedgerRepository) Append(ctx context.Context, entry *models.EarningsLedger) error {
	op := logging.Start(ctx, r.logger, "EarningsLedger.Append", logrus.Fields{
		"de_id": entry.DEID, "type": string(entry.Type), "amount": entry.AmountZMW,
	})
	defer op.End()

	if entry.CreatedAt == "" {
		entry.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	item, err := attributevalue.MarshalMap(entry)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal ledger entry: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: entry.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: entry.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to append ledger entry: %w", err))
	}
	return nil
}

// QueryByDE returns earning entries for a DE sorted by created_at descending (newest first).
func (r *EarningsLedgerRepository) QueryByDE(ctx context.Context, deID, afterTimestamp string, pageSize int32, lastKey map[string]types.AttributeValue) ([]*models.EarningsLedger, map[string]types.AttributeValue, error) {
	op := logging.Start(ctx, r.logger, "EarningsLedger.QueryByDE", logrus.Fields{"de_id": deID})
	defer op.End()

	input := &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		KeyConditionExpression: aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: "EARN!" + deID},
		},
		ScanIndexForward:  aws.Bool(false),
		Limit:             aws.Int32(pageSize),
		ExclusiveStartKey: lastKey,
	}

	if afterTimestamp != "" {
		input.KeyConditionExpression = aws.String("PK = :pk AND SK > :after")
		input.ExpressionAttributeValues[":after"] = &types.AttributeValueMemberS{Value: afterTimestamp}
	}

	result, err := r.client.Query(ctx, input)
	if err != nil {
		return nil, nil, op.Fail(fmt.Errorf("failed to query ledger: %w", err))
	}

	var entries []*models.EarningsLedger
	for _, item := range result.Items {
		var entry models.EarningsLedger
		if err := attributevalue.UnmarshalMap(item, &entry); err != nil {
			op.Logger().WithError(err).Warn("failed to unmarshal ledger entry; skipping")
			continue
		}
		entries = append(entries, &entry)
	}

	op.With("count", len(entries))
	return entries, result.LastEvaluatedKey, nil
}

// SumByDEAfter sums AmountZMW for all ledger entries for a DE after a given timestamp.
func (r *EarningsLedgerRepository) SumByDEAfter(ctx context.Context, deID, afterTimestamp string) (float64, error) {
	op := logging.Start(ctx, r.logger, "EarningsLedger.SumByDEAfter", logrus.Fields{"de_id": deID})
	defer op.End()

	var total float64
	var lastKey map[string]types.AttributeValue

	for {
		entries, nextKey, err := r.QueryByDE(ctx, deID, afterTimestamp, 50, lastKey)
		if err != nil {
			return 0, op.Fail(err)
		}
		for _, e := range entries {
			total += e.AmountZMW
		}
		if nextKey == nil {
			break
		}
		lastKey = nextKey
	}

	op.With("total_zmw", total)
	return total, nil
}

// SumPositiveCashByDEAfter sums payable cash entries for a DE after a given timestamp.
func (r *EarningsLedgerRepository) SumPositiveCashByDEAfter(ctx context.Context, deID, afterTimestamp string) (float64, error) {
	op := logging.Start(ctx, r.logger, "EarningsLedger.SumPositiveCashByDEAfter", logrus.Fields{"de_id": deID})
	defer op.End()

	var total float64
	var lastKey map[string]types.AttributeValue

	for {
		entries, nextKey, err := r.QueryByDE(ctx, deID, afterTimestamp, 50, lastKey)
		if err != nil {
			return 0, op.Fail(err)
		}
		for _, e := range entries {
			if models.IsPositiveCashEarning(e) {
				total += e.AmountZMW
			}
		}
		if nextKey == nil {
			break
		}
		lastKey = nextKey
	}

	op.With("total_zmw", total)
	return total, nil
}

// ExistsByReference reports whether a DE already has a ledger entry for a
// specific earning type + reference window key.
//
// This is the idempotency guard for cron-emitted rewards. It must NOT use a
// DynamoDB Query Limit together with a FilterExpression: DynamoDB applies Limit
// BEFORE the filter, so a Limit:1 query inspects only the single lowest-SK item
// in the EARN!{deID} partition and then filters — the reward row is almost never
// that earliest item, so the match is missed and the reward is re-emitted on
// every run. Instead this paginates the whole partition via LastEvaluatedKey
// (mirroring SumByDEAfter above) and matches the reward type + reference in Go,
// returning true as soon as any page yields a match.
func (r *EarningsLedgerRepository) ExistsByReference(ctx context.Context, deID string, earningType models.EarningType, referenceID string) (bool, error) {
	op := logging.Start(ctx, r.logger, "EarningsLedger.ExistsByReference", logrus.Fields{
		"de_id": deID, "type": string(earningType), "reference_id": referenceID,
	})
	defer op.End()

	var lastKey map[string]types.AttributeValue
	for {
		entries, nextKey, err := r.QueryByDE(ctx, deID, "", 50, lastKey)
		if err != nil {
			return false, op.Fail(err)
		}
		for _, e := range entries {
			if e.Type == earningType && e.ReferenceID == referenceID {
				op.With("exists", true)
				return true, nil
			}
		}
		if nextKey == nil {
			break
		}
		lastKey = nextKey
	}

	op.With("exists", false)
	return false, nil
}
