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
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":  "Store",
		"jti": tokenData.JTI,
	}).Info("dynamodb call start")

	// Calculate TTL (expiration time in Unix seconds)
	ttl := tokenData.ExpiresAt.Unix()

	item := map[string]types.AttributeValue{
		"PK":         &types.AttributeValueMemberS{Value: fmt.Sprintf("REFRESH_TOKEN#%s", tokenData.JTI)},
		"SK":         &types.AttributeValueMemberS{Value: "METADATA"},
		"JTI":        &types.AttributeValueMemberS{Value: tokenData.JTI},
		"EntityID":   &types.AttributeValueMemberS{Value: tokenData.EntityID},
		"EntityType": &types.AttributeValueMemberS{Value: tokenData.EntityType},
		"Phone":      &types.AttributeValueMemberS{Value: tokenData.Phone},
		"FamilyID":   &types.AttributeValueMemberS{Value: tokenData.FamilyID},
		"Revoked":    &types.AttributeValueMemberBOOL{Value: tokenData.Revoked},
		"CreatedAt":  &types.AttributeValueMemberS{Value: tokenData.CreatedAt.Format(time.RFC3339)},
		"ExpiresAt":  &types.AttributeValueMemberS{Value: tokenData.ExpiresAt.Format(time.RFC3339)},
		"TTL":        &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", ttl)},
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
		return fmt.Errorf("failed to store refresh token: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "Store",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("dynamodb call done")
	return nil
}

// Get retrieves refresh token from DynamoDB
func (r *RefreshTokenRepository) Get(ctx context.Context, jti string) (*models.RefreshTokenData, error) {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":  "Get",
		"jti": jti,
	}).Info("dynamodb call start")

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: fmt.Sprintf("REFRESH_TOKEN#%s", jti)},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})

	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Get",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to get refresh token: %w", err)
	}

	if result.Item == nil {
		log.WithFields(logrus.Fields{
			"op":          "Get",
			"duration_ms": time.Since(start).Milliseconds(),
			"found":       false,
		}).Info("dynamodb call done")
		return nil, fmt.Errorf("refresh token not found")
	}

	var tokenData models.RefreshTokenData
	if err := attributevalue.UnmarshalMap(result.Item, &tokenData); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Get",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to unmarshal token data: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "Get",
		"duration_ms": time.Since(start).Milliseconds(),
		"found":       true,
	}).Info("dynamodb call done")
	return &tokenData, nil
}

// Delete removes refresh token from DynamoDB
func (r *RefreshTokenRepository) Delete(ctx context.Context, jti string) error {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":  "Delete",
		"jti": jti,
	}).Info("dynamodb call start")

	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: fmt.Sprintf("REFRESH_TOKEN#%s", jti)},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})

	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Delete",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return fmt.Errorf("failed to delete refresh token: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "Delete",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("dynamodb call done")
	return nil
}

// IsRevoked checks if a token is revoked by checking for revoked marker
func (r *RefreshTokenRepository) IsRevoked(ctx context.Context, jti string) (bool, error) {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":  "IsRevoked",
		"jti": jti,
	}).Info("dynamodb call start")

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: fmt.Sprintf("REVOKED_TOKEN#%s", jti)},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})

	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "IsRevoked",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return false, err
	}

	revoked := result.Item != nil
	log.WithFields(logrus.Fields{
		"op":          "IsRevoked",
		"duration_ms": time.Since(start).Milliseconds(),
		"found":       revoked,
	}).Info("dynamodb call done")
	return revoked, nil
}

// MarkRevoked marks a token as revoked with TTL
func (r *RefreshTokenRepository) MarkRevoked(ctx context.Context, jti string, expiresAt time.Time) error {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":  "MarkRevoked",
		"jti": jti,
	}).Info("dynamodb call start")

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
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "MarkRevoked",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return fmt.Errorf("failed to mark token as revoked: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "MarkRevoked",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("dynamodb call done")
	return nil
}

// GetByFamilyID retrieves all tokens for a given family ID
func (r *RefreshTokenRepository) GetByFamilyID(ctx context.Context, familyID string) ([]models.RefreshTokenData, error) {
	log := logging.FromContext(ctx, r.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":        "GetByFamilyID",
		"family_id": familyID,
	}).Info("dynamodb call start")

	// Query using GSI (if you create one) or scan with filter expression
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
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GetByFamilyID",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to query tokens by family ID: %w", err)
	}

	var tokens []models.RefreshTokenData
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &tokens); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GetByFamilyID",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("dynamodb call failed")
		return nil, fmt.Errorf("failed to unmarshal tokens: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "GetByFamilyID",
		"duration_ms": time.Since(start).Milliseconds(),
		"count":       len(tokens),
	}).Info("dynamodb call done")
	return tokens, nil
}
