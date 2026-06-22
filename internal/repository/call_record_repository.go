package repository

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type CallRecordRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewCallRecordRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *CallRecordRepository {
	return &CallRecordRepository{client: client, tableName: tableName, logger: logger}
}

// Upsert writes (or overwrites) a CallRecord under PK="TRIP!"+TripID, SK="CALL!"+CallID.
func (r *CallRecordRepository) Upsert(ctx context.Context, rec *models.CallRecord) error {
	op := logging.Start(ctx, r.logger, "CallRecordRepository.Upsert", logrus.Fields{
		"trip_id": rec.TripID,
		"call_id": rec.CallID,
	})
	defer op.End()

	item, err := attributevalue.MarshalMap(rec)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal call record: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: rec.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: rec.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to upsert call record: %w", err))
	}
	return nil
}

// CountByTripDirection returns the number of calls for a trip in the given direction.
// It queries all CALL! items under the trip partition key and counts those whose
// `direction` attribute matches the supplied direction argument.
func (r *CallRecordRepository) CountByTripDirection(ctx context.Context, tripID, direction string) (int, error) {
	op := logging.Start(ctx, r.logger, "CallRecordRepository.CountByTripDirection", logrus.Fields{
		"trip_id":   tripID,
		"direction": direction,
	})
	defer op.End()

	pk := "TRIP!" + tripID
	skPrefix := "CALL!"

	var count int
	var lastKey map[string]types.AttributeValue

	for {
		input := &dynamodb.QueryInput{
			TableName:              aws.String(r.tableName),
			KeyConditionExpression: aws.String("PK = :pk AND begins_with(SK, :sk)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk": &types.AttributeValueMemberS{Value: pk},
				":sk": &types.AttributeValueMemberS{Value: skPrefix},
			},
		}
		if lastKey != nil {
			input.ExclusiveStartKey = lastKey
		}

		out, err := r.client.Query(ctx, input)
		if err != nil {
			return 0, op.Fail(fmt.Errorf("failed to query call records: %w", err))
		}

		for _, item := range out.Items {
			var rec models.CallRecord
			if err := attributevalue.UnmarshalMap(item, &rec); err != nil {
				return 0, op.Fail(fmt.Errorf("failed to unmarshal call record: %w", err))
			}
			if rec.Direction == direction {
				count++
			}
		}

		if out.LastEvaluatedKey == nil {
			break
		}
		lastKey = out.LastEvaluatedKey
	}

	return count, nil
}
