package repository

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

type PageRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewPageRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *PageRepository {
	return &PageRepository{
		client:    client,
		tableName: tableName,
		logger:    logger,
	}
}

// GetPageByKey fetches page data from DynamoDB by partition key
func (r *PageRepository) GetPageByKey(ctx context.Context, pk string) (map[string]interface{}, error) {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op": "GetPageByKey",
		"pk": pk,
	}).Info("dynamodb call start")

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: pk},
			"SK": &types.AttributeValueMemberS{Value: pk}, // Using same value for SK
		},
	})

	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GetPageByKey",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to get page: %w", err)
	}

	if result.Item == nil {
		log.WithFields(logrus.Fields{
			"op":          "GetPageByKey",
			"duration_ms": time.Since(start).Milliseconds(),
			"found":       false,
		}).Info("dynamodb call done")
		return nil, nil // Page not found
	}

	// Convert DynamoDB item to map
	pageData := make(map[string]interface{})
	for key, value := range result.Item {
		pageData[key] = convertAttributeValue(value)
	}

	log.WithFields(logrus.Fields{
		"op":          "GetPageByKey",
		"duration_ms": time.Since(start).Milliseconds(),
		"found":       true,
	}).Info("dynamodb call done")
	return pageData, nil
}

func convertAttributeValue(av types.AttributeValue) interface{} {
	switch v := av.(type) {

	case *types.AttributeValueMemberS:
		return v.Value

	case *types.AttributeValueMemberN:
		// DynamoDB numbers are strings → parse explicitly
		if strings.Contains(v.Value, ".") {
			if f, err := strconv.ParseFloat(v.Value, 64); err == nil {
				return f
			}
			return v.Value
		}

		if i, err := strconv.ParseInt(v.Value, 10, 64); err == nil {
			return i
		}

		return v.Value

	case *types.AttributeValueMemberBOOL:
		return v.Value

	case *types.AttributeValueMemberNULL:
		return nil

	case *types.AttributeValueMemberM:
		m := make(map[string]interface{}, len(v.Value))
		for k, val := range v.Value {
			m[k] = convertAttributeValue(val)
		}
		return m

	case *types.AttributeValueMemberL:
		l := make([]interface{}, len(v.Value))
		for i, val := range v.Value {
			l[i] = convertAttributeValue(val)
		}
		return l

	default:
		return nil
	}
}
