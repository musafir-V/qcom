package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type RefreshTokenRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewRefreshTokenRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *RefreshTokenRepository {
	return &RefreshTokenRepository{
		client:    client,
		tableName: tableName,
		logger:    logger,
	}
}

// Store stores refresh token in DynamoDB with TTL
func (r *RefreshTokenRepository) Store(ctx context.Context, tokenData models.RefreshTokenData) error {
	// Calculate TTL (expiration time in Unix seconds)
	ttl := tokenData.ExpiresAt.Unix()

	item := map[string]types.AttributeValue{
		"PK":        &types.AttributeValueMemberS{Value: fmt.Sprintf("REFRESH_TOKEN#%s", tokenData.JTI)},
		"SK":        &types.AttributeValueMemberS{Value: "METADATA"},
		"JTI":       &types.AttributeValueMemberS{Value: tokenData.JTI},
		"UserID":    &types.AttributeValueMemberS{Value: tokenData.UserID},
		"Phone":     &types.AttributeValueMemberS{Value: tokenData.Phone},
		"FamilyID":  &types.AttributeValueMemberS{Value: tokenData.FamilyID},
		"Revoked":   &types.AttributeValueMemberBOOL{Value: tokenData.Revoked},
		"CreatedAt": &types.AttributeValueMemberS{Value: tokenData.CreatedAt.Format(time.RFC3339)},
		"ExpiresAt": &types.AttributeValueMemberS{Value: tokenData.ExpiresAt.Format(time.RFC3339)},
		"TTL":       &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", ttl)},
	}

	_, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})

	if err != nil {
		r.logger.WithError(err).Error("Failed to store refresh token in DynamoDB")
		return fmt.Errorf("failed to store refresh token: %w", err)
	}

	return nil
}

// Get retrieves refresh token from DynamoDB
func (r *RefreshTokenRepository) Get(ctx context.Context, jti string) (*models.RefreshTokenData, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: fmt.Sprintf("REFRESH_TOKEN#%s", jti)},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})

	if err != nil {
		return nil, fmt.Errorf("failed to get refresh token: %w", err)
	}

	if result.Item == nil {
		return nil, fmt.Errorf("refresh token not found")
	}

	var tokenData models.RefreshTokenData
	if err := attributevalue.UnmarshalMap(result.Item, &tokenData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal token data: %w", err)
	}

	return &tokenData, nil
}

// Delete removes refresh token from DynamoDB
func (r *RefreshTokenRepository) Delete(ctx context.Context, jti string) error {
	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: fmt.Sprintf("REFRESH_TOKEN#%s", jti)},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})

	if err != nil {
		return fmt.Errorf("failed to delete refresh token: %w", err)
	}

	return nil
}

// IsRevoked checks if a token is revoked by checking for revoked marker
func (r *RefreshTokenRepository) IsRevoked(ctx context.Context, jti string) (bool, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: fmt.Sprintf("REVOKED_TOKEN#%s", jti)},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})

	if err != nil {
		return false, err
	}

	return result.Item != nil, nil
}

// MarkRevoked marks a token as revoked with TTL
func (r *RefreshTokenRepository) MarkRevoked(ctx context.Context, jti string, expiresAt time.Time) error {
	ttl := expiresAt.Unix()

	item := map[string]types.AttributeValue{
		"PK":        &types.AttributeValueMemberS{Value: fmt.Sprintf("REVOKED_TOKEN#%s", jti)},
		"SK":        &types.AttributeValueMemberS{Value: "METADATA"},
		"RevokedAt": &types.AttributeValueMemberS{Value: time.Now().Format(time.RFC3339)},
		"TTL":       &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", ttl)},
	}

	_, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})

	if err != nil {
		return fmt.Errorf("failed to mark token as revoked: %w", err)
	}

	return nil
}

// GetByFamilyID retrieves all tokens for a given family ID
func (r *RefreshTokenRepository) GetByFamilyID(ctx context.Context, familyID string) ([]models.RefreshTokenData, error) {
	// Query using GSI (if you create one) or scan with filter
	// For simplicity, using scan with filter expression
	result, err := r.client.Scan(ctx, &dynamodb.ScanInput{
		TableName:        aws.String(r.tableName),
		FilterExpression: aws.String("begins_with(PK, :pk_prefix) AND FamilyID = :family_id"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk_prefix": &types.AttributeValueMemberS{Value: "REFRESH_TOKEN#"},
			":family_id": &types.AttributeValueMemberS{Value: familyID},
		},
	})

	if err != nil {
		return nil, fmt.Errorf("failed to query tokens by family ID: %w", err)
	}

	var tokens []models.RefreshTokenData
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &tokens); err != nil {
		return nil, fmt.Errorf("failed to unmarshal tokens: %w", err)
	}

	return tokens, nil
}
