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

type AddressRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewAddressRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *AddressRepository {
	return &AddressRepository{
		client:    client,
		tableName: tableName,
		logger:    logger,
	}
}

func (r *AddressRepository) Create(ctx context.Context, address *models.Address) error {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":         "Create",
		"address_id": address.AddressID,
	}).Info("dynamodb call start")

	item, err := attributevalue.MarshalMap(address)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Create",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return fmt.Errorf("failed to marshal address: %w", err)
	}

	item["PK"] = &types.AttributeValueMemberS{Value: address.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: address.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Create",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return fmt.Errorf("failed to create address: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "Create",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("dynamodb call done")
	return nil
}

func (r *AddressRepository) GetByID(ctx context.Context, addressID string) (*models.Address, error) {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":         "GetByID",
		"address_id": addressID,
	}).Info("dynamodb call start")

	addr := &models.Address{AddressID: addressID}

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: addr.GetPK()},
			"SK": &types.AttributeValueMemberS{Value: addr.GetSK()},
		},
	})
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GetByID",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to get address: %w", err)
	}

	if result.Item == nil {
		log.WithFields(logrus.Fields{
			"op":          "GetByID",
			"duration_ms": time.Since(start).Milliseconds(),
			"found":       false,
		}).Info("dynamodb call done")
		return nil, nil
	}

	var dbAddr models.Address
	if err := attributevalue.UnmarshalMap(result.Item, &dbAddr); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GetByID",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to unmarshal address: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "GetByID",
		"duration_ms": time.Since(start).Milliseconds(),
		"found":       true,
	}).Info("dynamodb call done")
	return &dbAddr, nil
}

func (r *AddressRepository) QueryByUserID(ctx context.Context, userID string) ([]models.Address, error) {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":      "QueryByUserID",
		"user_id": userID,
	}).Info("dynamodb call start")

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		IndexName:              aws.String("UserIdIndex"),
		KeyConditionExpression: aws.String("user_id = :uid"),
		FilterExpression:       aws.String("is_active = :active"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":uid":    &types.AttributeValueMemberS{Value: userID},
			":active": &types.AttributeValueMemberBOOL{Value: true},
		},
	})
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "QueryByUserID",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to query addresses: %w", err)
	}

	var addresses []models.Address
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &addresses); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "QueryByUserID",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to unmarshal addresses: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "QueryByUserID",
		"duration_ms": time.Since(start).Milliseconds(),
		"count":       len(addresses),
	}).Info("dynamodb call done")
	return addresses, nil
}

func (r *AddressRepository) UpdateReceiverDetails(ctx context.Context, addressID string, updates map[string]string) (*models.Address, error) {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":         "UpdateReceiverDetails",
		"address_id": addressID,
	}).Info("dynamodb call start")

	addr := &models.Address{AddressID: addressID}

	updateExpr := "SET updated_at = :updated_at"
	exprAttrValues := map[string]types.AttributeValue{}
	exprAttrNames := map[string]string{}

	if name, ok := updates["receiver_name"]; ok {
		updateExpr += ", receiver_name = :rname"
		exprAttrValues[":rname"] = &types.AttributeValueMemberS{Value: name}
	}
	if phone, ok := updates["receiver_phone"]; ok {
		updateExpr += ", receiver_phone = :rphone"
		exprAttrValues[":rphone"] = &types.AttributeValueMemberS{Value: phone}
	}

	exprAttrValues[":updated_at"] = &types.AttributeValueMemberS{Value: updates["updated_at"]}

	input := &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: addr.GetPK()},
			"SK": &types.AttributeValueMemberS{Value: addr.GetSK()},
		},
		UpdateExpression:          aws.String(updateExpr),
		ExpressionAttributeValues: exprAttrValues,
		ReturnValues:              types.ReturnValueAllNew,
	}
	if len(exprAttrNames) > 0 {
		input.ExpressionAttributeNames = exprAttrNames
	}

	result, err := r.client.UpdateItem(ctx, input)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "UpdateReceiverDetails",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to update address: %w", err)
	}

	var updated models.Address
	if err := attributevalue.UnmarshalMap(result.Attributes, &updated); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "UpdateReceiverDetails",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to unmarshal updated address: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "UpdateReceiverDetails",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("dynamodb call done")
	return &updated, nil
}

func (r *AddressRepository) SoftDelete(ctx context.Context, addressID, updatedAt string) error {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":         "SoftDelete",
		"address_id": addressID,
	}).Info("dynamodb call start")

	addr := &models.Address{AddressID: addressID}

	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: addr.GetPK()},
			"SK": &types.AttributeValueMemberS{Value: addr.GetSK()},
		},
		UpdateExpression: aws.String("SET is_active = :inactive, updated_at = :updated_at"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":inactive":   &types.AttributeValueMemberBOOL{Value: false},
			":updated_at": &types.AttributeValueMemberS{Value: updatedAt},
		},
	})
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "SoftDelete",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return fmt.Errorf("failed to soft-delete address: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "SoftDelete",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("dynamodb call done")
	return nil
}
