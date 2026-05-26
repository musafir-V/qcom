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

type OTPRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewOTPRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *OTPRepository {
	return &OTPRepository{
		client:    client,
		tableName: tableName,
		logger:    logger,
	}
}

// Store stores OTP data in DynamoDB with TTL
func (r *OTPRepository) Store(ctx context.Context, phoneNumber string, otpData models.OTPData) error {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":    "Store",
		"phone": phoneNumber,
	}).Info("dynamodb call start")

	// Calculate TTL (expiration time in Unix seconds)
	ttl := otpData.ExpiresAt.Unix()

	item := map[string]types.AttributeValue{
		"PK":        &types.AttributeValueMemberS{Value: fmt.Sprintf("OTP#%s", phoneNumber)},
		"SK":        &types.AttributeValueMemberS{Value: "METADATA"},
		"OTPHash":   &types.AttributeValueMemberS{Value: otpData.OTPHash},
		"Phone":     &types.AttributeValueMemberS{Value: otpData.Phone},
		"Attempts":  &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", otpData.Attempts)},
		"CreatedAt": &types.AttributeValueMemberS{Value: otpData.CreatedAt.Format(time.RFC3339)},
		"ExpiresAt": &types.AttributeValueMemberS{Value: otpData.ExpiresAt.Format(time.RFC3339)},
		"TTL":       &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", ttl)},
	}

	_, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})

	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Store",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return fmt.Errorf("failed to store OTP: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "Store",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("dynamodb call done")
	return nil
}

// Get retrieves OTP data from DynamoDB
func (r *OTPRepository) Get(ctx context.Context, phoneNumber string) (*models.OTPData, error) {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":    "Get",
		"phone": phoneNumber,
	}).Info("dynamodb call start")

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: fmt.Sprintf("OTP#%s", phoneNumber)},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})

	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Get",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to get OTP: %w", err)
	}

	if result.Item == nil {
		log.WithFields(logrus.Fields{
			"op":          "Get",
			"duration_ms": time.Since(start).Milliseconds(),
			"found":       false,
		}).Info("dynamodb call done")
		return nil, fmt.Errorf("OTP not found or expired")
	}

	var otpData models.OTPData
	if err := attributevalue.UnmarshalMap(result.Item, &otpData); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Get",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to unmarshal OTP data: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "Get",
		"duration_ms": time.Since(start).Milliseconds(),
		"found":       true,
	}).Info("dynamodb call done")
	return &otpData, nil
}

// Delete removes OTP data from DynamoDB
func (r *OTPRepository) Delete(ctx context.Context, phoneNumber string) error {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":    "Delete",
		"phone": phoneNumber,
	}).Info("dynamodb call start")

	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: fmt.Sprintf("OTP#%s", phoneNumber)},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})

	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Delete",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return fmt.Errorf("failed to delete OTP: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "Delete",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("dynamodb call done")
	return nil
}

// StoreTestOTP stores plain OTP for testing purposes
func (r *OTPRepository) StoreTestOTP(ctx context.Context, phoneNumber, otp string, expiresAt time.Time) error {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":    "StoreTestOTP",
		"phone": phoneNumber,
	}).Info("dynamodb call start")

	ttl := expiresAt.Unix()

	item := map[string]types.AttributeValue{
		"PK":        &types.AttributeValueMemberS{Value: fmt.Sprintf("OTP_TEST#%s", phoneNumber)},
		"SK":        &types.AttributeValueMemberS{Value: "METADATA"},
		"OTP":       &types.AttributeValueMemberS{Value: otp},
		"ExpiresAt": &types.AttributeValueMemberS{Value: expiresAt.Format(time.RFC3339)},
		"TTL":       &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", ttl)},
	}

	_, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})

	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "StoreTestOTP",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return fmt.Errorf("failed to store test OTP: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "StoreTestOTP",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("dynamodb call done")
	return nil
}
