package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

type RefreshTokenService struct {
	tokenRepo *repository.RefreshTokenRepository
	logger    *logrus.Logger
}

func NewRefreshTokenService(tokenRepo *repository.RefreshTokenRepository, logger *logrus.Logger) *RefreshTokenService {
	return &RefreshTokenService{
		tokenRepo: tokenRepo,
		logger:    logger,
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

	return s.tokenRepo.Store(ctx, tokenData)
}

func (s *RefreshTokenService) Get(ctx context.Context, jti string) (*models.RefreshTokenData, error) {
	return s.tokenRepo.Get(ctx, jti)
}

func (s *RefreshTokenService) Revoke(ctx context.Context, jti string) error {
	tokenData, err := s.Get(ctx, jti)
	if err != nil {
		return err
	}

	tokenData.Revoked = true
	if err := s.tokenRepo.Store(ctx, *tokenData); err != nil {
		return fmt.Errorf("failed to revoke refresh token: %w", err)
	}

	// Also mark as revoked for quick lookup
	if err := s.tokenRepo.MarkRevoked(ctx, jti, tokenData.ExpiresAt); err != nil {
		return fmt.Errorf("failed to mark token as revoked: %w", err)
	}

	return nil
}

func (s *RefreshTokenService) IsRevoked(ctx context.Context, jti string) (bool, error) {
	return s.tokenRepo.IsRevoked(ctx, jti)
}

func (s *RefreshTokenService) RevokeFamily(ctx context.Context, familyID string) error {
	tokens, err := s.tokenRepo.GetByFamilyID(ctx, familyID)
	if err != nil {
		return err
	}

	for _, token := range tokens {
		if err := s.Revoke(ctx, token.JTI); err != nil {
			s.logger.WithError(err).WithField("jti", token.JTI).Error("Failed to revoke token in family")
		}
	}

	return nil
}

func GenerateFamilyID() string {
	return uuid.New().String()
}
