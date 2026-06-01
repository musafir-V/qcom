package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

const cronLockPK = "CRON_LOCK"
const cronLockSK = "trip-assignment"

type CronLockRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewCronLockRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *CronLockRepository {
	return &CronLockRepository{client: client, tableName: tableName, logger: logger}
}

// Acquire attempts to acquire the distributed cron lock.
// Returns true if acquired, false if another instance holds it.
// The lock expires after ttlSeconds if Release is never called (e.g. instance crash).
func (r *CronLockRepository) Acquire(ctx context.Context, ttlSeconds int) (bool, error) {
	op := logging.Start(ctx, r.logger, "CronLock.Acquire", nil)
	defer op.End()

	expiresAt := time.Now().UTC().Add(time.Duration(ttlSeconds) * time.Second).Format(time.RFC3339)

	_, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item: map[string]types.AttributeValue{
			"PK":         &types.AttributeValueMemberS{Value: cronLockPK},
			"SK":         &types.AttributeValueMemberS{Value: cronLockSK},
			"expires_at": &types.AttributeValueMemberS{Value: expiresAt},
		},
		// Acquire if: lock doesn't exist OR it has expired
		ConditionExpression: aws.String("attribute_not_exists(PK) OR expires_at < :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":now": &types.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339)},
		},
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			op.With("acquired", false)
			return false, nil // another instance holds the lock
		}
		return false, op.Fail(fmt.Errorf("failed to acquire cron lock: %w", err))
	}

	op.With("acquired", true)
	return true, nil
}

// Release deletes the lock so the next tick can acquire it immediately.
// Always call this after a successful tick completes (even on partial failure).
func (r *CronLockRepository) Release(ctx context.Context) error {
	op := logging.Start(ctx, r.logger, "CronLock.Release", nil)
	defer op.End()

	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: cronLockPK},
			"SK": &types.AttributeValueMemberS{Value: cronLockSK},
		},
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to release cron lock: %w", err))
	}
	return nil
}
