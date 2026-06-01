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
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type WeeklySummaryRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewWeeklySummaryRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *WeeklySummaryRepository {
	return &WeeklySummaryRepository{client: client, tableName: tableName, logger: logger}
}

// Create writes a new weekly summary; idempotent via attribute_not_exists.
func (r *WeeklySummaryRepository) Create(ctx context.Context, summary *models.DEWeeklySummary) error {
	op := logging.Start(ctx, r.logger, "WeeklySummary.Create", logrus.Fields{
		"de_id": summary.DEID, "week": summary.WeekStartDate,
	})
	defer op.End()

	summary.CreatedAt = time.Now().UTC().Format(time.RFC3339)

	item, err := attributevalue.MarshalMap(summary)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal weekly summary: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: summary.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: summary.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(r.tableName),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(PK) AND attribute_not_exists(SK)"),
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return op.Outcome("already_exists", fmt.Errorf("weekly summary already exists for DE %s week %s", summary.DEID, summary.WeekStartDate))
		}
		return op.Fail(fmt.Errorf("failed to create weekly summary: %w", err))
	}
	return nil
}

// GetByWeek fetches a weekly summary for a specific DE and week start date.
func (r *WeeklySummaryRepository) GetByWeek(ctx context.Context, deID, weekStartDate string) (*models.DEWeeklySummary, error) {
	op := logging.Start(ctx, r.logger, "WeeklySummary.GetByWeek", logrus.Fields{
		"de_id": deID, "week": weekStartDate,
	})
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "WEEKLY!" + deID},
			"SK": &types.AttributeValueMemberS{Value: weekStartDate},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get weekly summary: %w", err))
	}
	if result.Item == nil {
		return nil, nil
	}

	var summary models.DEWeeklySummary
	if err := attributevalue.UnmarshalMap(result.Item, &summary); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal weekly summary: %w", err))
	}
	return &summary, nil
}
