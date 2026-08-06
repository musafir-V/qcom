package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

const refreshReuseGrace = 60 * time.Second

var ErrRefreshTokenRevoked = errors.New("refresh token revoked")

// MintFunc mints a new pair for familyID and returns pair, familyID, newJTI, newExpiresAt.
type MintFunc func(familyID string) (*models.TokenPair, string, string, time.Time, error)

// refreshTokenStore is the persistence surface used by RefreshTokenService.
// *repository.RefreshTokenRepository satisfies this interface.
type refreshTokenStore interface {
	Store(ctx context.Context, tokenData models.RefreshTokenData) error
	Get(ctx context.Context, jti string) (*models.RefreshTokenData, error)
	IsRevoked(ctx context.Context, jti string) (bool, error)
	MarkRevoked(ctx context.Context, jti string, expiresAt time.Time) error
	TryMarkRevoked(ctx context.Context, jti string, expiresAt time.Time) (bool, error)
	StoreReplacement(ctx context.Context, rep models.RefreshReplacement, graceTTL time.Duration) error
	GetReplacement(ctx context.Context, oldJTI string) (*models.RefreshReplacement, error)
	GetByFamilyID(ctx context.Context, familyID string) ([]models.RefreshTokenData, error)
	GetByEntityID(ctx context.Context, entityID string) ([]models.RefreshTokenData, error)
}

type RefreshTokenService struct {
	tokenRepo    refreshTokenStore
	logger       *logrus.Logger
	pollAttempts int
	pollDelay    time.Duration
}

func NewRefreshTokenService(tokenRepo *repository.RefreshTokenRepository, logger *logrus.Logger) *RefreshTokenService {
	return &RefreshTokenService{
		tokenRepo:    tokenRepo,
		logger:       logger,
		pollAttempts: 10,
		pollDelay:    50 * time.Millisecond,
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

// RevokeAllForEntity revokes every refresh token owned by an entity across all
// devices/families. Used on account deletion so no lingering session can mint
// new access tokens for the deleted account.
func (s *RefreshTokenService) RevokeAllForEntity(ctx context.Context, entityID string) error {
	op := logging.Start(ctx, s.logger, "RevokeAllForEntity", logrus.Fields{"entity_id": entityID})
	defer op.End()

	tokens, err := s.tokenRepo.GetByEntityID(ctx, entityID)
	if err != nil {
		return op.Fail(err)
	}

	for _, token := range tokens {
		if err := s.Revoke(ctx, token.JTI); err != nil {
			op.Logger().WithError(err).WithField("jti", token.JTI).Error("Failed to revoke token for entity")
		}
	}

	op.With("count", len(tokens))
	return nil
}

// Rotate idempotently rotates a refresh token. Concurrent callers with the same
// oldJTI within refreshReuseGrace receive the same TokenPair. Reuse after the
// grace window (or after hard Revoke/logout) returns ErrRefreshTokenRevoked.
func (s *RefreshTokenService) Rotate(
	ctx context.Context,
	oldJTI, entityID, entityType, phone string,
	tokenExpiresAt time.Time,
	mint MintFunc,
) (*models.TokenPair, error) {
	op := logging.Start(ctx, s.logger, "Rotate", logrus.Fields{"jti": oldJTI})
	defer op.End()

	pollAttempts := s.pollAttempts
	if pollAttempts <= 0 {
		pollAttempts = 10
	}
	pollDelay := s.pollDelay
	if pollDelay <= 0 {
		pollDelay = 50 * time.Millisecond
	}

	var knownFamilyID string

	// 1. Cached replacement within grace → return same pair (idempotent retry).
	rep, err := s.tokenRepo.GetReplacement(ctx, oldJTI)
	if err != nil {
		return nil, op.Fail(err)
	}
	if rep != nil {
		if time.Since(rep.IssuedAt) <= refreshReuseGrace {
			pair := rep.TokenPair()
			return &pair, nil
		}
		// Outside grace: treat as missing (theft path). Keep family for step 5.
		knownFamilyID = rep.FamilyID
	}

	// 2. Atomically claim rotation.
	claimed, err := s.tokenRepo.TryMarkRevoked(ctx, oldJTI, tokenExpiresAt)
	if err != nil {
		return nil, op.Fail(err)
	}

	if claimed {
		// 3. Winner path.
		familyID := knownFamilyID
		oldData, getErr := s.tokenRepo.Get(ctx, oldJTI)
		if getErr == nil && oldData != nil {
			familyID = oldData.FamilyID
		}

		pair, newFamilyID, newJTI, newExpiresAt, err := mint(familyID)
		if err != nil {
			return nil, op.Fail(err)
		}

		if err := s.tokenRepo.Store(ctx, models.RefreshTokenData{
			JTI:        newJTI,
			EntityID:   entityID,
			EntityType: entityType,
			Phone:      phone,
			FamilyID:   newFamilyID,
			CreatedAt:  time.Now(),
			ExpiresAt:  newExpiresAt,
			Revoked:    false,
		}); err != nil {
			return nil, op.Fail(err)
		}

		// Optionally mark old REFRESH_TOKEN metadata revoked if Get succeeded.
		if getErr == nil && oldData != nil {
			oldData.Revoked = true
			_ = s.tokenRepo.Store(ctx, *oldData)
		}

		replacement := models.RefreshReplacement{
			OldJTI:       oldJTI,
			AccessToken:  pair.AccessToken,
			RefreshToken: pair.RefreshToken,
			TokenType:    pair.TokenType,
			ExpiresIn:    pair.ExpiresIn,
			FamilyID:     newFamilyID,
			NewJTI:       newJTI,
			IssuedAt:     time.Now(),
		}
		if err := s.tokenRepo.StoreReplacement(ctx, replacement, refreshReuseGrace); err != nil {
			return nil, op.Fail(err)
		}
		return pair, nil
	}

	// 4. Loser path: poll for winner's replacement within grace.
	for i := 0; i < pollAttempts; i++ {
		rep, err := s.tokenRepo.GetReplacement(ctx, oldJTI)
		if err != nil {
			return nil, op.Fail(err)
		}
		if rep != nil {
			if knownFamilyID == "" {
				knownFamilyID = rep.FamilyID
			}
			if time.Since(rep.IssuedAt) <= refreshReuseGrace {
				pair := rep.TokenPair()
				return &pair, nil
			}
		}
		if i < pollAttempts-1 {
			time.Sleep(pollDelay)
		}
	}

	// 5. Reuse outside grace / logout: revoke family if known, then deny.
	if knownFamilyID == "" {
		if oldData, getErr := s.tokenRepo.Get(ctx, oldJTI); getErr == nil && oldData != nil {
			knownFamilyID = oldData.FamilyID
		}
	}
	if knownFamilyID != "" {
		_ = s.RevokeFamily(ctx, knownFamilyID)
	}
	return nil, op.Outcome("revoked", ErrRefreshTokenRevoked)
}

func GenerateFamilyID() string {
	return uuid.New().String()
}
