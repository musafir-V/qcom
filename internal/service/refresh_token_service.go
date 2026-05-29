package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/logging"
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

func (s *RefreshTokenService) Store(ctx context.Context, jti, entityID, entityType, phone, familyID string, expiresAt time.Time) error {
	op := logging.Start(ctx, s.logger, "Store", logrus.Fields{
		"jti":         jti,
		"entity_id":   entityID,
		"entity_type": entityType,
	})
	defer op.End()

	tokenData := models.RefreshTokenData{
		JTI:        jti,
		EntityID:   entityID,
		EntityType: entityType,
		Phone:      phone,
		FamilyID:   familyID,
		CreatedAt:  time.Now(),
		ExpiresAt:  expiresAt,
		Revoked:    false,
	}

	if err := s.tokenRepo.Store(ctx, tokenData); err != nil {
		return op.Fail(err)
	}
	return nil
}

func (s *RefreshTokenService) Get(ctx context.Context, jti string) (*models.RefreshTokenData, error) {
	op := logging.Start(ctx, s.logger, "Get", logrus.Fields{"jti": jti})
	defer op.End()

	data, err := s.tokenRepo.Get(ctx, jti)
	if err != nil {
		return nil, op.Fail(err)
	}
	op.With("found", data != nil)
	return data, nil
}

func (s *RefreshTokenService) Revoke(ctx context.Context, jti string) error {
	op := logging.Start(ctx, s.logger, "Revoke", logrus.Fields{"jti": jti})
	defer op.End()

	tokenData, err := s.Get(ctx, jti)
	if err != nil {
		return op.Fail(err)
	}

	tokenData.Revoked = true
	if err := s.tokenRepo.Store(ctx, *tokenData); err != nil {
		return op.Fail(fmt.Errorf("failed to revoke refresh token: %w", err))
	}

	if err := s.tokenRepo.MarkRevoked(ctx, jti, tokenData.ExpiresAt); err != nil {
		return op.Fail(fmt.Errorf("failed to mark token as revoked: %w", err))
	}
	return nil
}

func (s *RefreshTokenService) IsRevoked(ctx context.Context, jti string) (bool, error) {
	op := logging.Start(ctx, s.logger, "IsRevoked", logrus.Fields{"jti": jti})
	defer op.End()

	revoked, err := s.tokenRepo.IsRevoked(ctx, jti)
	if err != nil {
		return false, op.Fail(err)
	}
	op.With("revoked", revoked)
	return revoked, nil
}

func (s *RefreshTokenService) RevokeFamily(ctx context.Context, familyID string) error {
	op := logging.Start(ctx, s.logger, "RevokeFamily", logrus.Fields{"family_id": familyID})
	defer op.End()

	tokens, err := s.tokenRepo.GetByFamilyID(ctx, familyID)
	if err != nil {
		return op.Fail(err)
	}

	for _, token := range tokens {
		if err := s.Revoke(ctx, token.JTI); err != nil {
			op.Logger().WithError(err).WithField("jti", token.JTI).Error("Failed to revoke token in family")
		}
	}

	op.With("count", len(tokens))
	return nil
}

func GenerateFamilyID() string {
	return uuid.New().String()
}
