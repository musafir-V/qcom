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
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":          "Store",
		"jti":         jti,
		"entity_id":   entityID,
		"entity_type": entityType,
	}).Info("service call start")

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
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Store",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return err
	}

	log.WithFields(logrus.Fields{
		"op":          "Store",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("service call done")
	return nil
}

func (s *RefreshTokenService) Get(ctx context.Context, jti string) (*models.RefreshTokenData, error) {
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":  "Get",
		"jti": jti,
	}).Info("service call start")

	data, err := s.tokenRepo.Get(ctx, jti)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Get",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return nil, err
	}

	log.WithFields(logrus.Fields{
		"op":          "Get",
		"duration_ms": time.Since(start).Milliseconds(),
		"found":       data != nil,
	}).Info("service call done")
	return data, nil
}

func (s *RefreshTokenService) Revoke(ctx context.Context, jti string) error {
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":  "Revoke",
		"jti": jti,
	}).Info("service call start")

	tokenData, err := s.Get(ctx, jti)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Revoke",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return err
	}

	tokenData.Revoked = true
	if err := s.tokenRepo.Store(ctx, *tokenData); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Revoke",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return fmt.Errorf("failed to revoke refresh token: %w", err)
	}

	if err := s.tokenRepo.MarkRevoked(ctx, jti, tokenData.ExpiresAt); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Revoke",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return fmt.Errorf("failed to mark token as revoked: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "Revoke",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("service call done")
	return nil
}

func (s *RefreshTokenService) IsRevoked(ctx context.Context, jti string) (bool, error) {
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":  "IsRevoked",
		"jti": jti,
	}).Info("service call start")

	revoked, err := s.tokenRepo.IsRevoked(ctx, jti)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "IsRevoked",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return false, err
	}

	log.WithFields(logrus.Fields{
		"op":          "IsRevoked",
		"duration_ms": time.Since(start).Milliseconds(),
		"revoked":     revoked,
	}).Info("service call done")
	return revoked, nil
}

func (s *RefreshTokenService) RevokeFamily(ctx context.Context, familyID string) error {
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":        "RevokeFamily",
		"family_id": familyID,
	}).Info("service call start")

	tokens, err := s.tokenRepo.GetByFamilyID(ctx, familyID)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "RevokeFamily",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return err
	}

	for _, token := range tokens {
		if err := s.Revoke(ctx, token.JTI); err != nil {
			log.WithError(err).WithField("jti", token.JTI).Error("Failed to revoke token in family")
		}
	}

	log.WithFields(logrus.Fields{
		"op":          "RevokeFamily",
		"duration_ms": time.Since(start).Milliseconds(),
		"count":       len(tokens),
	}).Info("service call done")
	return nil
}

func GenerateFamilyID() string {
	return uuid.New().String()
}
