package service

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

type ReferralService struct {
	referralRepo     *repository.ReferralRepository
	deRepo           *repository.DERepository
	payoutConfigRepo *repository.PayoutConfigRepository
	logger           *logrus.Logger
}

func NewReferralService(
	referralRepo *repository.ReferralRepository,
	deRepo *repository.DERepository,
	payoutConfigRepo *repository.PayoutConfigRepository,
	logger *logrus.Logger,
) *ReferralService {
	return &ReferralService{
		referralRepo:     referralRepo,
		deRepo:           deRepo,
		payoutConfigRepo: payoutConfigRepo,
		logger:           logger,
	}
}

// GenerateUniqueCode generates a 6-digit numeric code that is not already
// used by another DE. Retries up to 10 times before returning an error.
func (s *ReferralService) GenerateUniqueCode(ctx context.Context) (string, error) {
	op := logging.Start(ctx, s.logger, "ReferralService.GenerateUniqueCode", nil)
	defer op.End()

	for attempt := 0; attempt < 10; attempt++ {
		code := generateReferralCode()
		existing, err := s.deRepo.GetByReferralCode(ctx, code)
		if err != nil {
			return "", op.Fail(fmt.Errorf("failed to check referral code uniqueness: %w", err))
		}
		if existing == nil {
			return code, nil
		}
	}
	return "", op.Fail(fmt.Errorf("failed to generate unique referral code after 10 attempts"))
}

// LinkReferral creates the referral relationship when a new DE registers with a referral code.
// referredDEID is the UUID of the newly registered DE.
// referralCode is the code the new DE provided during registration.
func (s *ReferralService) LinkReferral(ctx context.Context, referredDEID, referralCode string, windowDays int) error {
	op := logging.Start(ctx, s.logger, "ReferralService.LinkReferral", logrus.Fields{
		"referred_de_id": referredDEID,
		"referral_code":  referralCode,
	})
	defer op.End()

	referrer, err := s.deRepo.GetByReferralCode(ctx, referralCode)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to look up referrer: %w", err))
	}
	if referrer == nil {
		return op.Outcome("invalid_code", fmt.Errorf("referral code %q not found", referralCode))
	}

	now := time.Now().UTC()
	windowExpires := now.Add(time.Duration(windowDays) * 24 * time.Hour).Format(time.RFC3339)

	ref := &models.Referral{
		ReferrerDEID:    referrer.DEID,
		ReferredDEID:    referredDEID,
		Status:          models.ReferralStatusActive,
		WindowExpiresAt: windowExpires,
	}
	if err := s.referralRepo.Create(ctx, ref); err != nil {
		return op.Fail(fmt.Errorf("failed to create referral: %w", err))
	}

	op.With("referrer_de_id", referrer.DEID)
	return nil
}

// CheckAndTriggerBonus is called after every trip completion for the DE.
// totalTripsCompleted is the DE's all-time completed trip count.
// If the DE has an active referral and has hit the threshold within the window,
// it marks the referral completed and returns the bonus amount to credit to both DEs.
// Returns (bonusZMW, referrerDEID, error). bonusZMW=0 means no bonus triggered.
func (s *ReferralService) CheckAndTriggerBonus(ctx context.Context, referredDEID string, totalTripsCompleted int) (float64, string, error) {
	op := logging.Start(ctx, s.logger, "ReferralService.CheckAndTriggerBonus", logrus.Fields{
		"referred_de_id":        referredDEID,
		"total_trips_completed": totalTripsCompleted,
	})
	defer op.End()

	cfg, err := s.payoutConfigRepo.Get(ctx)
	if err != nil {
		return 0, "", op.Fail(fmt.Errorf("failed to get payout config: %w", err))
	}

	if totalTripsCompleted < cfg.ReferralTripsThreshold {
		return 0, "", nil
	}

	ref, err := s.referralRepo.GetByReferredDEID(ctx, referredDEID)
	if err != nil {
		return 0, "", op.Fail(fmt.Errorf("failed to get referral: %w", err))
	}
	if ref == nil || ref.Status != models.ReferralStatusActive {
		return 0, "", nil
	}

	if !isWithinReferralWindow(ref.CreatedAt, cfg.ReferralWindowDays) {
		return 0, "", nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if err := s.referralRepo.MarkCompleted(ctx, referredDEID, now); err != nil {
		// "already_completed" outcome means a concurrent call won the race — not an error
		if !strings.Contains(err.Error(), "already completed") {
			return 0, "", op.Fail(err)
		}
		return 0, "", nil
	}

	op.With("bonus_zmw", cfg.ReferralBonusZMW).With("referrer_de_id", ref.ReferrerDEID)
	return cfg.ReferralBonusZMW, ref.ReferrerDEID, nil
}

// GetReferralScreen returns the DE's referral code and list of referrals they initiated.
func (s *ReferralService) GetReferralScreen(ctx context.Context, deID, dePhone string) (string, []*models.Referral, error) {
	op := logging.Start(ctx, s.logger, "ReferralService.GetReferralScreen", logrus.Fields{"de_id": deID})
	defer op.End()

	de, err := s.deRepo.GetByPhone(ctx, dePhone)
	if err != nil || de == nil {
		return "", nil, op.Fail(fmt.Errorf("failed to fetch DE: %w", err))
	}

	refs, err := s.referralRepo.ListByReferrerDEID(ctx, deID)
	if err != nil {
		return "", nil, op.Fail(err)
	}

	return de.ReferralCode, refs, nil
}

// generateReferralCode returns a random 6-digit numeric string (000000–999999).
func generateReferralCode() string {
	return fmt.Sprintf("%06d", rand.Intn(1000000))
}

// isWithinReferralWindow returns true if createdAt is within windowDays days of now.
func isWithinReferralWindow(createdAt string, windowDays int) bool {
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return false
	}
	return time.Since(t) <= time.Duration(windowDays)*24*time.Hour
}
