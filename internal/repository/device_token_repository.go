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

type DeviceTokenRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewDeviceTokenRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *DeviceTokenRepository {
	return &DeviceTokenRepository{client: client, tableName: tableName, logger: logger}
}

func (r *DeviceTokenRepository) Get(ctx context.Context, recipientType models.RecipientType, recipientID string) (*models.DeviceTokenRecord, error) {
	op := logging.Start(ctx, r.logger, "DeviceTokenRepository.Get", logrus.Fields{
		"recipient_type": recipientType,
		"recipient_id":   recipientID,
	})
	defer op.End()

	record := &models.DeviceTokenRecord{
		RecipientType: recipientType,
		RecipientID:   recipientID,
	}
	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: record.GetPK()},
			"SK": &types.AttributeValueMemberS{Value: record.GetSK()},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get device token: %w", err))
	}
	if out.Item == nil {
		return nil, nil
	}
	if err := attributevalue.UnmarshalMap(out.Item, record); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal device token: %w", err))
	}
	return record, nil
}

func (r *DeviceTokenRepository) Upsert(ctx context.Context, recipientType models.RecipientType, recipientID, token, platform string) error {
	op := logging.Start(ctx, r.logger, "DeviceTokenRepository.Upsert", logrus.Fields{
		"recipient_type": recipientType,
		"recipient_id":   recipientID,
	})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)
	record := &models.DeviceTokenRecord{
		RecipientType: recipientType,
		RecipientID:   recipientID,
		FCMToken:      token,
		Platform:      platform,
		UpdatedAt:     now,
	}
	item, err := attributevalue.MarshalMap(record)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal device token: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: record.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: record.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to upsert device token: %w", err))
	}
	return nil
}

func (r *DeviceTokenRepository) Delete(ctx context.Context, recipientType models.RecipientType, recipientID string) error {
	op := logging.Start(ctx, r.logger, "DeviceTokenRepository.Delete", logrus.Fields{
		"recipient_type": recipientType,
		"recipient_id":   recipientID,
	})
	defer op.End()

	record := &models.DeviceTokenRecord{
		RecipientType: recipientType,
		RecipientID:   recipientID,
	}
	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: record.GetPK()},
			"SK": &types.AttributeValueMemberS{Value: record.GetSK()},
		},
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to delete device token: %w", err))
	}
	return nil
}
