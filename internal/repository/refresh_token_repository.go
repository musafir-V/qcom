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
	op := logging.Start(ctx, r.logger, "Store", logrus.Fields{"jti": tokenData.JTI})
	defer op.End()

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
		return op.Fail(fmt.Errorf("failed to store refresh token: %w", err))
	}
	return nil
}

// Get retrieves refresh token from DynamoDB
func (r *RefreshTokenRepository) Get(ctx context.Context, jti string) (*models.RefreshTokenData, error) {
	op := logging.Start(ctx, r.logger, "Get", logrus.Fields{"jti": jti})
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: fmt.Sprintf("REFRESH_TOKEN#%s", jti)},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get refresh token: %w", err))
	}

	if result.Item == nil {
		return nil, op.Outcome("not_found", fmt.Errorf("refresh token not found"))
	}

	var tokenData models.RefreshTokenData
	if err := attributevalue.UnmarshalMap(result.Item, &tokenData); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal token data: %w", err))
	}

	op.With("found", true)
	return &tokenData, nil
}

// Delete removes refresh token from DynamoDB
func (r *RefreshTokenRepository) Delete(ctx context.Context, jti string) error {
	op := logging.Start(ctx, r.logger, "Delete", logrus.Fields{"jti": jti})
	defer op.End()

	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: fmt.Sprintf("REFRESH_TOKEN#%s", jti)},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to delete refresh token: %w", err))
	}
	return nil
}

// IsRevoked checks if a token is revoked by checking for revoked marker
func (r *RefreshTokenRepository) IsRevoked(ctx context.Context, jti string) (bool, error) {
	op := logging.Start(ctx, r.logger, "IsRevoked", logrus.Fields{"jti": jti})
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: fmt.Sprintf("REVOKED_TOKEN#%s", jti)},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil {
		return false, op.Fail(err)
	}

	revoked := result.Item != nil
	op.With("revoked", revoked)
	return revoked, nil
}

// MarkRevoked marks a token as revoked with TTL
func (r *RefreshTokenRepository) MarkRevoked(ctx context.Context, jti string, expiresAt time.Time) error {
	op := logging.Start(ctx, r.logger, "MarkRevoked", logrus.Fields{"jti": jti})
	defer op.End()

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
		return op.Fail(fmt.Errorf("failed to mark token as revoked: %w", err))
	}
	return nil
}

// TryMarkRevoked atomically claims rotation for jti. Returns claimed=true if this
// caller created the REVOKED marker; claimed=false if another caller already did.
func (r *RefreshTokenRepository) TryMarkRevoked(ctx context.Context, jti string, expiresAt time.Time) (bool, error) {
	op := logging.Start(ctx, r.logger, "TryMarkRevoked", logrus.Fields{"jti": jti})
	defer op.End()

	ttl := expiresAt.Unix()
	item := map[string]types.AttributeValue{
		"PK":        &types.AttributeValueMemberS{Value: fmt.Sprintf("REVOKED_TOKEN#%s", jti)},
		"SK":        &types.AttributeValueMemberS{Value: "METADATA"},
		"RevokedAt": &types.AttributeValueMemberS{Value: time.Now().Format(time.RFC3339)},
		"TTL":       &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", ttl)},
	}

	_, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(r.tableName),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
	})
	if err != nil {
		var ccf *types.ConditionalCheckFailedException
		if errors.As(err, &ccf) {
			op.With("claimed", false)
			return false, nil
		}
		return false, op.Fail(fmt.Errorf("failed to claim refresh revocation: %w", err))
	}
	op.With("claimed", true)
	return true, nil
}

// StoreReplacement persists the token pair issued for a rotated refresh JTI.
// graceTTL controls DynamoDB item expiry (reuse window for concurrent clients).
func (r *RefreshTokenRepository) StoreReplacement(ctx context.Context, rep models.RefreshReplacement, graceTTL time.Duration) error {
	op := logging.Start(ctx, r.logger, "StoreReplacement", logrus.Fields{"old_jti": rep.OldJTI, "new_jti": rep.NewJTI})
	defer op.End()

	if graceTTL <= 0 {
		graceTTL = time.Minute
	}
	ttl := time.Now().Add(graceTTL).Unix()

	item := map[string]types.AttributeValue{
		"PK":           &types.AttributeValueMemberS{Value: fmt.Sprintf("REFRESH_REPLACEMENT#%s", rep.OldJTI)},
		"SK":           &types.AttributeValueMemberS{Value: "METADATA"},
		"OldJTI":       &types.AttributeValueMemberS{Value: rep.OldJTI},
		"AccessToken":  &types.AttributeValueMemberS{Value: rep.AccessToken},
		"RefreshToken": &types.AttributeValueMemberS{Value: rep.RefreshToken},
		"TokenType":    &types.AttributeValueMemberS{Value: rep.TokenType},
		"ExpiresIn":    &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", rep.ExpiresIn)},
		"FamilyID":     &types.AttributeValueMemberS{Value: rep.FamilyID},
		"NewJTI":       &types.AttributeValueMemberS{Value: rep.NewJTI},
		"IssuedAt":     &types.AttributeValueMemberS{Value: rep.IssuedAt.Format(time.RFC3339)},
		"TTL":          &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", ttl)},
	}

	_, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to store refresh replacement: %w", err))
	}
	return nil
}

// GetReplacement returns the cached replacement for oldJTI, or (nil, nil) if absent.
func (r *RefreshTokenRepository) GetReplacement(ctx context.Context, oldJTI string) (*models.RefreshReplacement, error) {
	op := logging.Start(ctx, r.logger, "GetReplacement", logrus.Fields{"old_jti": oldJTI})
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: fmt.Sprintf("REFRESH_REPLACEMENT#%s", oldJTI)},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get refresh replacement: %w", err))
	}
	if result.Item == nil {
		op.With("found", false)
		return nil, nil
	}

	var rep models.RefreshReplacement
	if err := attributevalue.UnmarshalMap(result.Item, &rep); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal refresh replacement: %w", err))
	}
	// IssuedAt may arrive as string; attributevalue usually handles time if tagged.
	if rep.IssuedAt.IsZero() {
		if raw, ok := result.Item["IssuedAt"].(*types.AttributeValueMemberS); ok {
			if t, err := time.Parse(time.RFC3339, raw.Value); err == nil {
				rep.IssuedAt = t
			}
		}
	}
	op.With("found", true)
	return &rep, nil
}

// GetByFamilyID retrieves all tokens for a given family ID
func (r *RefreshTokenRepository) GetByFamilyID(ctx context.Context, familyID string) ([]models.RefreshTokenData, error) {
	op := logging.Start(ctx, r.logger, "GetByFamilyID", logrus.Fields{"family_id": familyID})
	defer op.End()

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
		return nil, op.Fail(fmt.Errorf("failed to query tokens by family ID: %w", err))
	}

	var tokens []models.RefreshTokenData
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &tokens); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal tokens: %w", err))
	}

	op.With("count", len(tokens))
	return tokens, nil
}

// GetByEntityID retrieves all refresh tokens belonging to an entity (user/DE),
// across every token family/device. Used to revoke every session on account
// deletion. Like GetByFamilyID, this is a filtered scan.
func (r *RefreshTokenRepository) GetByEntityID(ctx context.Context, entityID string) ([]models.RefreshTokenData, error) {
	op := logging.Start(ctx, r.logger, "GetByEntityID", logrus.Fields{"entity_id": entityID})
	defer op.End()

	result, err := r.client.Scan(ctx, &dynamodb.ScanInput{
		TableName:        aws.String(r.tableName),
		FilterExpression: aws.String("begins_with(PK, :pk_prefix) AND EntityID = :entity_id"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk_prefix": &types.AttributeValueMemberS{Value: "REFRESH_TOKEN#"},
			":entity_id": &types.AttributeValueMemberS{Value: entityID},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to query tokens by entity ID: %w", err))
	}

	var tokens []models.RefreshTokenData
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &tokens); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal tokens: %w", err))
	}

	op.With("count", len(tokens))
	return tokens, nil
}
