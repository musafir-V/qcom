package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

// DEStatusEventRepository persists the append-only DE status-event log used to
// reconstruct presence timelines and online-time.
type DEStatusEventRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewDEStatusEventRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *DEStatusEventRepository {
	return &DEStatusEventRepository{client: client, tableName: tableName, logger: logger}
}

// Append writes one status-event item. If event.TS is empty it is stamped with
// the current UTC time (RFC3339). A random id suffix keeps the sort key unique
// when several events share a timestamp.
func (r *DEStatusEventRepository) Append(ctx context.Context, event *models.DEStatusEvent) error {
	op := logging.Start(ctx, r.logger, "DEStatusEventRepository.Append", logrus.Fields{
		"phone": event.Phone, "reason": string(event.Reason),
	})
	defer op.End()

	if event.TS == "" {
		event.TS = time.Now().UTC().Format(time.RFC3339)
	}

	item, err := attributevalue.MarshalMap(event)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal status event: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: models.StatusEventPK(event.Phone)}
	item["SK"] = &types.AttributeValueMemberS{Value: models.StatusEventSK(event.TS, uuid.NewString())}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to append status event: %w", err))
	}
	return nil
}

// ListEventsForDay returns all status events for a DE that fall within the given
// Zambia calendar date ("2006-01-02"), ordered ascending by timestamp. Event
// timestamps are stored in UTC, so the Zambia-day boundaries are converted to
// UTC for the sort-key range query.
func (r *DEStatusEventRepository) ListEventsForDay(ctx context.Context, phone, zambiaDate string) ([]*models.DEStatusEvent, error) {
	op := logging.Start(ctx, r.logger, "DEStatusEventRepository.ListEventsForDay", logrus.Fields{
		"phone": phone, "date": zambiaDate,
	})
	defer op.End()

	loc := timezone.ZambiaLocation()
	dayStart, err := time.ParseInLocation("2006-01-02", zambiaDate, loc)
	if err != nil {
		return nil, op.Fail(fmt.Errorf("invalid date %q: %w", zambiaDate, err))
	}
	dayEnd := dayStart.AddDate(0, 0, 1)

	startSK := models.StatusEventSKPrefix(dayStart.UTC().Format(time.RFC3339))
	endSK := models.StatusEventSKPrefix(dayEnd.UTC().Format(time.RFC3339))

	var events []*models.DEStatusEvent
	var lastKey map[string]types.AttributeValue
	for {
		result, err := r.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(r.tableName),
			KeyConditionExpression: aws.String("PK = :pk AND SK BETWEEN :start AND :end"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk":    &types.AttributeValueMemberS{Value: models.StatusEventPK(phone)},
				":start": &types.AttributeValueMemberS{Value: startSK},
				":end":   &types.AttributeValueMemberS{Value: endSK},
			},
			ScanIndexForward:  aws.Bool(true),
			ExclusiveStartKey: lastKey,
		})
		if err != nil {
			return nil, op.Fail(fmt.Errorf("failed to query status events: %w", err))
		}

		for _, item := range result.Items {
			var e models.DEStatusEvent
			if err := attributevalue.UnmarshalMap(item, &e); err != nil {
				op.Logger().WithError(err).Warn("failed to unmarshal status event; skipping")
				continue
			}
			events = append(events, &e)
		}

		if len(result.LastEvaluatedKey) == 0 {
			break
		}
		lastKey = result.LastEvaluatedKey
	}

	op.With("count", len(events))
	return events, nil
}
