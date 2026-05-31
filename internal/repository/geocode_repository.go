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

// geocodeCacheTTLDays is the lifetime of a cached reverse-geocode entry.
// Sublocality/locality names change extremely rarely, so 30 days keeps the
// hit rate high without holding onto answers indefinitely.
const geocodeCacheTTLDays = 30

const geocodeCachePKPrefix = "GEOCODE_CACHE!"
const geocodeCacheSK = "ADDRESS"

type geocodeCacheItem struct {
	Address   string `dynamodbav:"address"`
	CreatedAt string `dynamodbav:"created_at"`
	TTL       int64  `dynamodbav:"ttl"`
}

type GeocodeCacheRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewGeocodeCacheRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *GeocodeCacheRepository {
	return &GeocodeCacheRepository{client: client, tableName: tableName, logger: logger}
}

func (r *GeocodeCacheRepository) Get(ctx context.Context, h3Cell string) (*string, error) {
	op := logging.Start(ctx, r.logger, "Get", logrus.Fields{"h3_cell": h3Cell})
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: geocodeCachePKPrefix + h3Cell},
			"SK": &types.AttributeValueMemberS{Value: geocodeCacheSK},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get geocode cache: %w", err))
	}
	if result.Item == nil {
		op.With("found", false)
		return nil, nil
	}

	var item geocodeCacheItem
	if err := attributevalue.UnmarshalMap(result.Item, &item); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal geocode cache: %w", err))
	}

	op.With("found", true)
	return &item.Address, nil
}

func (r *GeocodeCacheRepository) Save(ctx context.Context, h3Cell string, address string) error {
	op := logging.Start(ctx, r.logger, "Save", logrus.Fields{"h3_cell": h3Cell})
	defer op.End()

	now := time.Now().UTC()
	item := geocodeCacheItem{
		Address:   address,
		CreatedAt: now.Format(time.RFC3339),
		TTL:       now.Add(geocodeCacheTTLDays * 24 * time.Hour).Unix(),
	}

	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal geocode cache: %w", err))
	}
	av["PK"] = &types.AttributeValueMemberS{Value: geocodeCachePKPrefix + h3Cell}
	av["SK"] = &types.AttributeValueMemberS{Value: geocodeCacheSK}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      av,
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to save geocode cache: %w", err))
	}
	return nil
}
