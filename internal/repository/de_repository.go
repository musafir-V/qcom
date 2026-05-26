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

type DERepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewDERepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *DERepository {
	return &DERepository{client: client, tableName: tableName, logger: logger}
}

func (r *DERepository) Create(ctx context.Context, de *models.DeliveryExecutive) error {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":    "Create",
		"phone": de.PhoneNumber,
	}).Info("dynamodb call start")

	now := time.Now().UTC().Format(time.RFC3339)
	de.CreatedAt = now
	de.UpdatedAt = now
	de.Status = models.DEStatusOffline

	item, err := attributevalue.MarshalMap(de)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Create",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return fmt.Errorf("failed to marshal DE: %w", err)
	}
	item["PK"] = &types.AttributeValueMemberS{Value: de.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: de.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(r.tableName),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			log.WithError(err).WithFields(logrus.Fields{
				"op":          "Create",
				"duration_ms": time.Since(start).Milliseconds(),
			}).Error("dynamodb call failed")
			return fmt.Errorf("delivery executive already registered with this number")
		}
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Create",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return fmt.Errorf("failed to create DE: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "Create",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("dynamodb call done")
	return nil
}

func (r *DERepository) GetByPhone(ctx context.Context, phone string) (*models.DeliveryExecutive, error) {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":    "GetByPhone",
		"phone": phone,
	}).Info("dynamodb call start")

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GetByPhone",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to get DE: %w", err)
	}
	if result.Item == nil {
		log.WithFields(logrus.Fields{
			"op":          "GetByPhone",
			"duration_ms": time.Since(start).Milliseconds(),
			"found":       false,
		}).Info("dynamodb call done")
		return nil, nil
	}

	var de models.DeliveryExecutive
	if err := attributevalue.UnmarshalMap(result.Item, &de); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GetByPhone",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to unmarshal DE: %w", err)
	}
	de.PhoneNumber = phone

	log.WithFields(logrus.Fields{
		"op":          "GetByPhone",
		"duration_ms": time.Since(start).Milliseconds(),
		"found":       true,
	}).Info("dynamodb call done")
	return &de, nil
}

func (r *DERepository) Exists(ctx context.Context, phone string) (bool, error) {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":    "Exists",
		"phone": phone,
	}).Info("dynamodb call start")

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		ProjectionExpression: aws.String("PK"),
	})
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Exists",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return false, fmt.Errorf("failed to check DE existence: %w", err)
	}

	found := result.Item != nil
	log.WithFields(logrus.Fields{
		"op":          "Exists",
		"duration_ms": time.Since(start).Milliseconds(),
		"found":       found,
	}).Info("dynamodb call done")
	return found, nil
}

// UpdateStatus transitions the DE to a new status and updates related fields.
// Pass empty strings for storeID and orderID to clear those fields.
func (r *DERepository) UpdateStatus(ctx context.Context, phone string, status models.DEStatus, storeID, orderID string) error {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":     "UpdateStatus",
		"phone":  phone,
		"status": string(status),
	}).Info("dynamodb call start")

	now := time.Now().UTC().Format(time.RFC3339)

	// Build duty_index_key for the DEDutyIndex GSI (used by assignment cron)
	dutyIndexKey := ""
	if status == models.DEStatusEligible && storeID != "" {
		dutyIndexKey = "DE_ELIGIBLE#" + storeID
	}

	expr := "SET #status = :status, updated_at = :updated_at"
	names := map[string]string{"#status": "status"}
	values := map[string]types.AttributeValue{
		":status":     &types.AttributeValueMemberS{Value: string(status)},
		":updated_at": &types.AttributeValueMemberS{Value: now},
	}

	if storeID != "" {
		expr += ", current_store_id = :store_id"
		values[":store_id"] = &types.AttributeValueMemberS{Value: storeID}
	} else {
		expr += " REMOVE current_store_id"
	}

	if orderID != "" {
		expr += ", current_order_id = :order_id"
		values[":order_id"] = &types.AttributeValueMemberS{Value: orderID}
	} else {
		expr += ", current_order_id = :empty_order"
		values[":empty_order"] = &types.AttributeValueMemberS{Value: ""}
	}

	if dutyIndexKey != "" {
		expr += ", duty_index_key = :duty_key"
		values[":duty_key"] = &types.AttributeValueMemberS{Value: dutyIndexKey}
	} else {
		expr += " REMOVE duty_index_key"
	}

	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression:          aws.String(expr),
		ExpressionAttributeNames:  names,
		ExpressionAttributeValues: values,
	})
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "UpdateStatus",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return fmt.Errorf("failed to update DE status: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "UpdateStatus",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("dynamodb call done")
	return nil
}

// FindEligibleByStore returns all DEs with status=eligible at the given store.
// Uses a table scan with filter — add DEDutyIndex GSI for production scale.
func (r *DERepository) FindEligibleByStore(ctx context.Context, storeID string) ([]*models.DeliveryExecutive, error) {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":       "FindEligibleByStore",
		"store_id": storeID,
	}).Info("dynamodb call start")

	dutyKey := "DE_ELIGIBLE#" + storeID

	result, err := r.client.Scan(ctx, &dynamodb.ScanInput{
		TableName:        aws.String(r.tableName),
		FilterExpression: aws.String("duty_index_key = :duty_key"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":duty_key": &types.AttributeValueMemberS{Value: dutyKey},
		},
	})
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "FindEligibleByStore",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to find eligible DEs: %w", err)
	}

	var des []*models.DeliveryExecutive
	for _, item := range result.Items {
		var de models.DeliveryExecutive
		if err := attributevalue.UnmarshalMap(item, &de); err != nil {
			log.WithError(err).WithFields(logrus.Fields{
				"op":          "FindEligibleByStore",
				"duration_ms": time.Since(start).Milliseconds(),
			}).Warn("dynamodb call failed")
			continue
		}
		des = append(des, &de)
	}

	log.WithFields(logrus.Fields{
		"op":          "FindEligibleByStore",
		"duration_ms": time.Since(start).Milliseconds(),
		"count":       len(des),
	}).Info("dynamodb call done")
	return des, nil
}
