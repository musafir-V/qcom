package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/models"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

type RefreshTokenService struct {
	client *redis.Client
	logger *logrus.Logger
}

func NewRefreshTokenService(client *redis.Client, logger *logrus.Logger) *RefreshTokenService {
	return &RefreshTokenService{
		client: client,
		logger: logger,
	}
}

func (s *RefreshTokenService) Store(ctx context.Context, jti, userID, phone, familyID string, expiresAt time.Time) error {
	tokenData := models.RefreshTokenData{
		JTI:       jti,
		UserID:    userID,
		Phone:     phone,
		FamilyID:  familyID,
		CreatedAt: time.Now(),
		ExpiresAt: expiresAt,
		Revoked:   false,
	}

	dataJSON, err := json.Marshal(tokenData)
	if err != nil {
		return fmt.Errorf("failed to marshal token data: %w", err)
	}

	key := fmt.Sprintf("refresh_token:%s", jti)
	ttl := time.Until(expiresAt)

	if err := s.client.Set(ctx, key, dataJSON, ttl).Err(); err != nil {
		s.logger.WithError(err).Error("Failed to store refresh token")
		return fmt.Errorf("failed to store refresh token: %w", err)
	}

	return nil
}

func (s *RefreshTokenService) Get(ctx context.Context, jti string) (*models.RefreshTokenData, error) {
	key := fmt.Sprintf("refresh_token:%s", jti)

	dataJSON, err := s.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, fmt.Errorf("refresh token not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get refresh token: %w", err)
	}

	var tokenData models.RefreshTokenData
	if err := json.Unmarshal([]byte(dataJSON), &tokenData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal token data: %w", err)
	}

	return &tokenData, nil
}

func (s *RefreshTokenService) Revoke(ctx context.Context, jti string) error {
	key := fmt.Sprintf("refresh_token:%s", jti)

	tokenData, err := s.Get(ctx, jti)
	if err != nil {
		return err
	}

	tokenData.Revoked = true
	dataJSON, _ := json.Marshal(tokenData)

	ttl := time.Until(tokenData.ExpiresAt)
	if ttl < 0 {
		ttl = 0
	}

	if err := s.client.Set(ctx, key, dataJSON, ttl).Err(); err != nil {
		return fmt.Errorf("failed to revoke refresh token: %w", err)
	}

	// Also add to revoked tokens list for quick lookup
	revokedKey := fmt.Sprintf("revoked_token:%s", jti)
	s.client.Set(ctx, revokedKey, "1", ttl)

	return nil
}

func (s *RefreshTokenService) IsRevoked(ctx context.Context, jti string) (bool, error) {
	revokedKey := fmt.Sprintf("revoked_token:%s", jti)
	exists, err := s.client.Exists(ctx, revokedKey).Result()
	if err != nil {
		return false, err
	}
	return exists > 0, nil
}

func (s *RefreshTokenService) RevokeFamily(ctx context.Context, familyID string) error {
	// This is a simplified version - in production, you might want to store
	// a mapping of family_id to all tokens
	pattern := "refresh_token:*"
	keys, err := s.client.Keys(ctx, pattern).Result()
	if err != nil {
		return err
	}

	for _, key := range keys {
		dataJSON, err := s.client.Get(ctx, key).Result()
		if err != nil {
			continue
		}

		var tokenData models.RefreshTokenData
		if err := json.Unmarshal([]byte(dataJSON), &tokenData); err != nil {
			continue
		}

		if tokenData.FamilyID == familyID {
			s.Revoke(ctx, tokenData.JTI)
		}
	}

	return nil
}

func GenerateFamilyID() string {
	return uuid.New().String()
}

