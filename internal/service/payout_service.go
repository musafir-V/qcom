package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

type PayoutService struct {
	payoutConfigRepo   *repository.PayoutConfigRepository
	earningsLedgerRepo *repository.EarningsLedgerRepository
	deRepo             *repository.DERepository
	tripRepo           *repository.TripRepository
	referralService    *ReferralService
	logger             *logrus.Logger
}

func NewPayoutService(
	payoutConfigRepo *repository.PayoutConfigRepository,
	earningsLedgerRepo *repository.EarningsLedgerRepository,
	deRepo *repository.DERepository,
	tripRepo *repository.TripRepository,
	referralService *ReferralService,
	logger *logrus.Logger,
) *PayoutService {
	return &PayoutService{
		payoutConfigRepo:   payoutConfigRepo,
		earningsLedgerRepo: earningsLedgerRepo,
		deRepo:             deRepo,
		tripRepo:           tripRepo,
		referralService:    referralService,
		logger:             logger,
	}
}

// ComputeBasePayZMW returns base pay for a trip at creation time (no DB write).
func (s *PayoutService) ComputeBasePayZMW(ctx context.Context, distanceKM float64) (float64, error) {
	cfg, err := s.payoutConfigRepo.Get(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get payout config: %w", err)
	}
	return computeBasePay(distanceKM, cfg), nil
}

// OnTripCompleted computes tier bonus, stamps the trip, writes ledger entries, checks referral bonus.
func (s *PayoutService) OnTripCompleted(ctx context.Context, trip *models.Trip, dePhone string) {
	op := logging.Start(ctx, s.logger, "PayoutService.OnTripCompleted", logrus.Fields{
		"trip_id": trip.TripID, "de_phone": dePhone,
	})
	defer op.End()

	cfg, err := s.payoutConfigRepo.Get(ctx)
	if err != nil {
		s.logger.WithError(err).Error("payout: failed to get config on trip completion")
		return
	}

	de, err := s.deRepo.GetByPhone(ctx, dePhone)
	if err != nil || de == nil {
		s.logger.WithError(err).Error("payout: failed to fetch DE on trip completion")
		return
	}

	newDailyCount, err := s.deRepo.IncrementDailyCount(ctx, dePhone, timezone.DateString())
	if err != nil {
		s.logger.WithError(err).Error("payout: failed to increment daily count")
		return
	}

	bonusPayZMW := computeTierBonus(newDailyCount, cfg)
	totalPayZMW := trip.BasePayZMW + bonusPayZMW

	if err := s.tripRepo.UpdatePayout(ctx, trip.TripID,
		trip.DistanceKM, trip.BasePayZMW, bonusPayZMW, totalPayZMW, newDailyCount); err != nil {
		s.logger.WithError(err).Error("payout: failed to update trip payout fields")
	}

	now := timezone.Now().Format(time.RFC3339)
	tripEntry := &models.EarningsLedger{
		DEID:        de.DEID,
		EarningID:   uuid.New().String(),
		Type:        models.EarningTypeTrip,
		AmountZMW:   totalPayZMW,
		CreatedAt:   now,
		ReferenceID: trip.TripID,
	}
	if err := s.earningsLedgerRepo.Append(ctx, tripEntry); err != nil {
		s.logger.WithError(err).Error("payout: failed to write trip ledger entry")
	}

	updatedDE, err := s.deRepo.GetByPhone(ctx, dePhone)
	if err == nil && updatedDE != nil && s.referralService != nil {
		bonusZMW, referrerDEID, err := s.referralService.CheckAndTriggerBonus(
			ctx, updatedDE.DEID, updatedDE.TotalTripsCompleted)
		if err != nil {
			s.logger.WithError(err).Warn("payout: referral bonus check failed")
		} else if bonusZMW > 0 {
			s.writeReferralBonusEntries(ctx, updatedDE.DEID, referrerDEID, bonusZMW, now)
		}
	}
}

// WriteWeeklyBonusEntry writes a weekly consistency bonus ledger entry (used by the weekly cron in B3).
func (s *PayoutService) WriteWeeklyBonusEntry(ctx context.Context, deID, weekStartDate string, bonusZMW float64) error {
	entry := &models.EarningsLedger{
		DEID:        deID,
		EarningID:   uuid.New().String(),
		Type:        models.EarningTypeWeeklyBonus,
		AmountZMW:   bonusZMW,
		CreatedAt:   timezone.Now().Format(time.RFC3339),
		ReferenceID: weekStartDate,
	}
	return s.earningsLedgerRepo.Append(context.Background(), entry)
}

func (s *PayoutService) writeReferralBonusEntries(ctx context.Context, referredDEID, referrerDEID string, bonusZMW float64, now string) {
	for _, deID := range []string{referredDEID, referrerDEID} {
		entry := &models.EarningsLedger{
			DEID:        deID,
			EarningID:   uuid.New().String(),
			Type:        models.EarningTypeReferralBonus,
			AmountZMW:   bonusZMW,
			CreatedAt:   now,
			ReferenceID: referredDEID,
		}
		if err := s.earningsLedgerRepo.Append(ctx, entry); err != nil {
			s.logger.WithError(err).WithField("de_id", deID).
				Error("payout: failed to write referral bonus ledger entry")
		}
	}
}

func computeBasePay(distanceKM float64, cfg *models.PayoutConfig) float64 {
	return cfg.RatePerKmZMW * distanceKM
}

// computeTierBonus returns per-delivery bonus based on 1-indexed daily delivery rank.
func computeTierBonus(dailyRank int, cfg *models.PayoutConfig) float64 {
	switch {
	case dailyRank > cfg.Tier2Threshold:
		return cfg.Tier2BonusZMW
	case dailyRank > cfg.Tier1Threshold:
		return cfg.Tier1BonusZMW
	default:
		return 0
	}
}
