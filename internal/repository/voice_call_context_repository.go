package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

const voiceCallContextTTL = 2 * time.Hour

type VoiceCallContextRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewVoiceCallContextRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *VoiceCallContextRepository {
	return &VoiceCallContextRepository{client: client, tableName: tableName, logger: logger}
}

func voiceCallContextPK(id string) string { return "VOICECTX!" + id }

// PutCallContext stores order/trip metadata keyed by call UUID and conversation UUID
// so lifecycle event webhooks (which omit custom_data) can persist CallRecords.
func (r *VoiceCallContextRepository) PutCallContext(
	ctx context.Context,
	callUUID, conversationUUID, orderID, tripID, direction, caller string,
) error {
	op := logging.Start(ctx, r.logger, "VoiceCallContextRepository.PutCallContext", logrus.Fields{
		"call_uuid":           callUUID,
		"conversation_uuid":   conversationUUID,
		"order_id":            orderID,
		"trip_id":             tripID,
	})
	defer op.End()

	ttl := time.Now().Add(voiceCallContextTTL).Unix()
	for _, id := range []string{callUUID, conversationUUID} {
		if id == "" {
			continue
		}
		if err := r.putOne(ctx, id, orderID, tripID, direction, caller, ttl); err != nil {
			return op.Fail(err)
		}
	}
	return nil
}

func (r *VoiceCallContextRepository) putOne(
	ctx context.Context,
	id, orderID, tripID, direction, caller string,
	ttl int64,
) error {
	_, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item: map[string]types.AttributeValue{
			"PK":        &types.AttributeValueMemberS{Value: voiceCallContextPK(id)},
			"SK":        &types.AttributeValueMemberS{Value: "META"},
			"order_id":  &types.AttributeValueMemberS{Value: orderID},
			"trip_id":   &types.AttributeValueMemberS{Value: tripID},
			"direction": &types.AttributeValueMemberS{Value: direction},
			"caller":    &types.AttributeValueMemberS{Value: caller},
			"TTL":       &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", ttl)},
		},
	})
	if err != nil {
		return fmt.Errorf("put voice call context %q: %w", id, err)
	}
	return nil
}

// GetCallContext resolves order metadata by conversation UUID or call UUID.
func (r *VoiceCallContextRepository) GetCallContext(
	ctx context.Context,
	conversationUUID, callUUID string,
) (orderID, tripID, direction, caller string, found bool) {
	for _, id := range []string{conversationUUID, callUUID} {
		if id == "" {
			continue
		}
		orderID, tripID, direction, caller, found, err := r.getOne(ctx, id)
		if err != nil || !found {
			continue
		}
		return orderID, tripID, direction, caller, true
	}
	return "", "", "", "", false
}

func (r *VoiceCallContextRepository) getOne(
	ctx context.Context,
	id string,
) (orderID, tripID, direction, caller string, found bool, err error) {
	op := logging.Start(ctx, r.logger, "VoiceCallContextRepository.GetCallContext", logrus.Fields{
		"context_id": id,
	})
	defer op.End()

	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: voiceCallContextPK(id)},
			"SK": &types.AttributeValueMemberS{Value: "META"},
		},
	})
	if err != nil {
		return "", "", "", "", false, op.Fail(fmt.Errorf("get voice call context %q: %w", id, err))
	}
	if out.Item == nil {
		return "", "", "", "", false, nil
	}
	return attrS(out.Item, "order_id"),
		attrS(out.Item, "trip_id"),
		attrS(out.Item, "direction"),
		attrS(out.Item, "caller"),
		true, nil
}

func attrS(item map[string]types.AttributeValue, key string) string {
	if v, ok := item[key].(*types.AttributeValueMemberS); ok {
		return v.Value
	}
	return ""
}
