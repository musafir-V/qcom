package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

const vonageJWTPK = "VONAGE_JWT"
const vonageJWTSK = "TOKEN"

type VonageJWTRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewVonageJWTRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *VonageJWTRepository {
	return &VonageJWTRepository{client: client, tableName: tableName, logger: logger}
}

// Get returns the cached JWT string, or ("", nil) if not found/expired.
func (r *VonageJWTRepository) Get(ctx context.Context) (string, error) {
	op := logging.Start(ctx, r.logger, "VonageJWTRepository.Get", nil)
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: vonageJWTPK},
			"SK": &types.AttributeValueMemberS{Value: vonageJWTSK},
		},
	})
	if err != nil {
		return "", op.Fail(fmt.Errorf("failed to get Vonage JWT: %w", err))
	}
	if result.Item == nil {
		return "", nil
	}

	tokenAttr, ok := result.Item["Token"].(*types.AttributeValueMemberS)
	if !ok || tokenAttr.Value == "" {
		return "", nil
	}
	return tokenAttr.Value, nil
}

// Store writes the JWT with a 55-minute TTL (DynamoDB auto-deletes after expiry).
func (r *VonageJWTRepository) Store(ctx context.Context, jwt string) error {
	op := logging.Start(ctx, r.logger, "VonageJWTRepository.Store", nil)
	defer op.End()

	ttl := time.Now().Add(55 * time.Minute).Unix()

	_, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item: map[string]types.AttributeValue{
			"PK":    &types.AttributeValueMemberS{Value: vonageJWTPK},
			"SK":    &types.AttributeValueMemberS{Value: vonageJWTSK},
			"Token": &types.AttributeValueMemberS{Value: jwt},
			"TTL":   &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", ttl)},
		},
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to store Vonage JWT: %w", err))
	}
	return nil
}
