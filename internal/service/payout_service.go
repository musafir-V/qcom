package service

import (
	"context"
	"fmt"
	"time"

	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/money"
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

	payout := computeCompletionPayout(trip, cfg)
	trip.DistanceKM = payout.DistanceKM
	trip.BasePayZMW = payout.BasePayZMW
	trip.BonusPayZMW = payout.BonusPayZMW
	trip.TotalPayZMW = payout.TotalPayZMW
	trip.OnTime = payout.OnTime

	if err := s.tripRepo.UpdatePayout(ctx, trip.TripID,
		trip.DistanceKM, trip.BasePayZMW, trip.BonusPayZMW, trip.TotalPayZMW, newDailyCount, trip.OnTime); err != nil {
		s.logger.WithError(err).Error("payout: failed to update trip payout fields")
	}

	now := timezone.Now().Format(time.RFC3339)
	tripEntry := &models.EarningsLedger{
		DEID:        de.DEID,
		Type:        models.EarningTypeTrip,
		AmountZMW:   trip.TotalPayZMW,
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

func (s *PayoutService) writeReferralBonusEntries(ctx context.Context, referredDEID, referrerDEID string, bonusZMW float64, now string) {
	amount := money.RoundUpZMW(bonusZMW)
	for _, deID := range []string{referredDEID, referrerDEID} {
		entry := &models.EarningsLedger{
			DEID:        deID,
			Type:        models.EarningTypeReferralBonus,
			AmountZMW:   amount,
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

type completionPayout struct {
	DistanceKM  float64
	BasePayZMW  float64
	BonusPayZMW float64
	TotalPayZMW float64
	OnTime      bool
}

func computeCompletionPayout(trip *models.Trip, cfg *models.PayoutConfig) completionPayout {
	distanceKM := trip.DistanceKM // Fallback to estimated distance; actual distance is not currently tracked.
	rawBasePayZMW := computeBasePay(distanceKM, cfg)
	totalPayZMW := ApplyRate(rawBasePayZMW, RateDecision{
		Multiplier: trip.RateMultiplier,
		FlatZMW:    trip.RateFlatZMW,
	})
	basePayZMW := money.Round2ZMW(rawBasePayZMW)
	bonusPayZMW := money.Round2ZMW(totalPayZMW - basePayZMW)

	actualMinutes, ok := trip.ActualDeliveryMinutes()
	onTime := ok && actualMinutes < trip.SLAMinutes

	return completionPayout{
		DistanceKM:  distanceKM,
		BasePayZMW:  basePayZMW,
		BonusPayZMW: bonusPayZMW,
		TotalPayZMW: totalPayZMW,
		OnTime:      onTime,
	}
}
