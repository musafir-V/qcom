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
	"github.com/sirupsen/logrus"
)

const etaCacheTTLHours = 24

type etaCacheItem struct {
	ETAMinutes int    `dynamodbav:"eta_minutes"`
	CreatedAt  string `dynamodbav:"created_at"`
	TTL        int64  `dynamodbav:"ttl"`
}

type ETACacheRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewETACacheRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *ETACacheRepository {
	return &ETACacheRepository{client: client, tableName: tableName, logger: logger}
}

func (r *ETACacheRepository) Get(ctx context.Context, h3Cell string) (*int, error) {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":      "Get",
		"h3_cell": h3Cell,
	}).Info("dynamodb call start")

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "ETA_CACHE!" + h3Cell},
			"SK": &types.AttributeValueMemberS{Value: "ETA"},
		},
	})
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Get",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to get eta cache: %w", err)
	}
	if result.Item == nil {
		log.WithFields(logrus.Fields{
			"op":          "Get",
			"duration_ms": time.Since(start).Milliseconds(),
			"found":       false,
		}).Info("dynamodb call done")
		return nil, nil
	}

	var item etaCacheItem
	if err := attributevalue.UnmarshalMap(result.Item, &item); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Get",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to unmarshal eta cache: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "Get",
		"duration_ms": time.Since(start).Milliseconds(),
		"found":       true,
	}).Info("dynamodb call done")
	return &item.ETAMinutes, nil
}

func (r *ETACacheRepository) Save(ctx context.Context, h3Cell string, etaMinutes int) error {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":      "Save",
		"h3_cell": h3Cell,
	}).Info("dynamodb call start")

	now := time.Now().UTC()
	item := etaCacheItem{
		ETAMinutes: etaMinutes,
		CreatedAt:  now.Format(time.RFC3339),
		TTL:        now.Add(etaCacheTTLHours * time.Hour).Unix(),
	}

	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Save",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return fmt.Errorf("failed to marshal eta cache: %w", err)
	}
	av["PK"] = &types.AttributeValueMemberS{Value: "ETA_CACHE!" + h3Cell}
	av["SK"] = &types.AttributeValueMemberS{Value: "ETA"}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      av,
	})
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Save",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return fmt.Errorf("failed to save eta cache: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "Save",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("dynamodb call done")
	return nil
}
