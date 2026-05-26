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
	"github.com/sirupsen/logrus"
)

type UserRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewUserRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *UserRepository {
	return &UserRepository{
		client:    client,
		tableName: tableName,
		logger:    logger,
	}
}

func (r *UserRepository) GetByPhoneNumber(ctx context.Context, phoneNumber string) (*models.User, error) {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":    "GetByPhoneNumber",
		"phone": phoneNumber,
	}).Info("dynamodb call start")

	user := &models.User{PhoneNumber: phoneNumber}
	pk := user.GetPK()
	sk := user.GetSK()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: pk},
			"SK": &types.AttributeValueMemberS{Value: sk},
		},
	})

	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GetByPhoneNumber",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	if result.Item == nil {
		log.WithFields(logrus.Fields{
			"op":          "GetByPhoneNumber",
			"duration_ms": time.Since(start).Milliseconds(),
			"found":       false,
		}).Info("dynamodb call done")
		return nil, nil // User not found
	}

	var dbUser models.User
	if err := attributevalue.UnmarshalMap(result.Item, &dbUser); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GetByPhoneNumber",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to unmarshal user: %w", err)
	}

	// Set PK and SK from the item
	if pkAttr, ok := result.Item["PK"].(*types.AttributeValueMemberS); ok {
		// Extract phone number from PK (USER!<phoneNumber>)
		if len(pkAttr.Value) > 5 {
			dbUser.PhoneNumber = pkAttr.Value[5:] // Remove "USER!" prefix
		}
	}

	log.WithFields(logrus.Fields{
		"op":          "GetByPhoneNumber",
		"duration_ms": time.Since(start).Milliseconds(),
		"found":       true,
	}).Info("dynamodb call done")
	return &dbUser, nil
}

func (r *UserRepository) Create(ctx context.Context, user *models.User) error {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":    "Create",
		"phone": user.PhoneNumber,
	}).Info("dynamodb call start")

	now := time.Now()
	user.UserID = uuid.New().String()
	user.CreatedAt = now
	user.UpdatedAt = now

	pk := user.GetPK()
	sk := user.GetSK()

	item, err := attributevalue.MarshalMap(user)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Create",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return fmt.Errorf("failed to marshal user: %w", err)
	}

	// Add PK and SK
	item["PK"] = &types.AttributeValueMemberS{Value: pk}
	item["SK"] = &types.AttributeValueMemberS{Value: sk}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(r.tableName),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
	})

	if err != nil {
		if _, ok := err.(*types.ConditionalCheckFailedException); ok {
			log.WithError(err).WithFields(logrus.Fields{
				"op":          "Create",
				"duration_ms": time.Since(start).Milliseconds(),
			}).Error("dynamodb call failed")
			return fmt.Errorf("user already exists")
		}
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Create",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return fmt.Errorf("failed to create user: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "Create",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("dynamodb call done")
	return nil
}

func (r *UserRepository) Update(ctx context.Context, user *models.User) error {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":    "Update",
		"phone": user.PhoneNumber,
	}).Info("dynamodb call start")

	user.UpdatedAt = time.Now()

	pk := user.GetPK()
	sk := user.GetSK()

	updateExpression := "SET #name = :name, updated_at = :updated_at"
	expressionAttributeNames := map[string]string{
		"#name": "name",
	}
	expressionAttributeValues := map[string]types.AttributeValue{
		":name":       &types.AttributeValueMemberS{Value: user.Name},
		":updated_at": &types.AttributeValueMemberS{Value: user.UpdatedAt.Format(time.RFC3339)},
	}

	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: pk},
			"SK": &types.AttributeValueMemberS{Value: sk},
		},
		UpdateExpression:          aws.String(updateExpression),
		ExpressionAttributeNames:  expressionAttributeNames,
		ExpressionAttributeValues: expressionAttributeValues,
	})

	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Update",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return fmt.Errorf("failed to update user: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "Update",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("dynamodb call done")
	return nil
}

func (r *UserRepository) GetOrCreate(ctx context.Context, phoneNumber string) (*models.User, error) {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":    "GetOrCreate",
		"phone": phoneNumber,
	}).Info("dynamodb call start")

	user, err := r.GetByPhoneNumber(ctx, phoneNumber)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GetOrCreate",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, err
	}

	if user != nil {
		log.WithFields(logrus.Fields{
			"op":          "GetOrCreate",
			"duration_ms": time.Since(start).Milliseconds(),
			"found":       true,
		}).Info("dynamodb call done")
		return user, nil
	}

	// User doesn't exist, create new one
	newUser := &models.User{
		PhoneNumber: phoneNumber,
		Name:        "", // Will be set later
	}

	if err := r.Create(ctx, newUser); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GetOrCreate",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, err
	}

	log.WithFields(logrus.Fields{
		"op":          "GetOrCreate",
		"duration_ms": time.Since(start).Milliseconds(),
		"found":       false,
	}).Info("dynamodb call done")
	return newUser, nil
}
