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

type SMSOTPRoutingConfigRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewSMSOTPRoutingConfigRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *SMSOTPRoutingConfigRepository {
	return &SMSOTPRoutingConfigRepository{client: client, tableName: tableName, logger: logger}
}

// Get returns the config. Missing row → ForceTwilio=false (split mode default).
func (r *SMSOTPRoutingConfigRepository) Get(ctx context.Context) (*models.SMSOTPRoutingConfig, error) {
	op := logging.Start(ctx, r.logger, "SMSOTPRoutingConfigRepository.Get", nil)
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "CONFIG"},
			"SK": &types.AttributeValueMemberS{Value: "SMS_OTP_ROUTING_V1"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get sms otp routing config: %w", err))
	}
	if result.Item == nil {
		return &models.SMSOTPRoutingConfig{ForceTwilio: false}, nil
	}
	var cfg models.SMSOTPRoutingConfig
	if err := attributevalue.UnmarshalMap(result.Item, &cfg); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal sms otp routing config: %w", err))
	}
	return &cfg, nil
}

// SetForceTwilio upserts the kill-switch.
func (r *SMSOTPRoutingConfigRepository) SetForceTwilio(ctx context.Context, force bool) error {
	op := logging.Start(ctx, r.logger, "SMSOTPRoutingConfigRepository.SetForceTwilio", logrus.Fields{
		"force_twilio": force,
	})
	defer op.End()

	cfg := models.SMSOTPRoutingConfig{ForceTwilio: force}
	item, err := attributevalue.MarshalMap(cfg)
	if err != nil {
		return op.Fail(fmt.Errorf("marshal sms otp routing config: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: cfg.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: cfg.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return op.Fail(fmt.Errorf("put sms otp routing config: %w", err))
	}
	return nil
}
