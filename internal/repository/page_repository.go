package repository

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
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
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: pk},
			"SK": &types.AttributeValueMemberS{Value: pk}, // Using same value for SK
		},
	})

	if err != nil {
		r.logger.WithError(err).WithField("pk", pk).Error("Failed to get page from DynamoDB")
		return nil, fmt.Errorf("failed to get page: %w", err)
	}

	if result.Item == nil {
		return nil, nil // Page not found
	}

	// Convert DynamoDB item to map
	pageData := make(map[string]interface{})
	for key, value := range result.Item {
		pageData[key] = convertAttributeValue(value)
	}

	return pageData, nil
}

// convertAttributeValue converts DynamoDB AttributeValue to Go native types
func convertAttributeValue(av types.AttributeValue) interface{} {
	switch v := av.(type) {
	case *types.AttributeValueMemberS:
		return v.Value
	case *types.AttributeValueMemberN:
		return v.Value
	case *types.AttributeValueMemberBOOL:
		return v.Value
	case *types.AttributeValueMemberNULL:
		return nil
	case *types.AttributeValueMemberM:
		m := make(map[string]interface{})
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

