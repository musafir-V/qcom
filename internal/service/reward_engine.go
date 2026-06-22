package service

import (
	"time"

	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/models"
)

// EvaluateAccumulator returns ledger entries to append for one window.
// trips = the DE's trips in the window; counts on_time and failures.
func EvaluateAccumulator(deID string, spec models.AccumulatorSpec, window string, trips []*models.Trip) []*models.EarningsLedger {
	onTimeCount, completedCount, cancelledCount := summarizeTrips(trips)
	if onTimeCount < spec.Threshold {
		return nil
	}
	if spec.RequireNoFail && cancelledCount > 0 {
		return nil
	}

	rate := computeOnTimeRate(onTimeCount, completedCount)
	if spec.MinOnTimeRate > 0 && rate < spec.MinOnTimeRate {
		return nil
	}

	earningType, ok := rewardEarningType(spec.Reward)
	if !ok {
		return nil
	}

	return []*models.EarningsLedger{
		{
			DEID:        deID,
			EarningID:   uuid.New().String(),
			Type:        earningType,
			AmountZMW:   spec.Reward.AmountZMW,
			Label:       spec.Reward.Label,
			CreatedAt:   time.Now().UTC().Format(time.RFC3339),
			ReferenceID: window,
		},
	}
}

func summarizeTrips(trips []*models.Trip) (onTimeCount, completedCount, cancelledCount int) {
	for _, trip := range trips {
		if trip == nil {
			continue
		}
		switch trip.Status {
		case models.TripStatusCompleted:
			completedCount++
			if trip.OnTime {
				onTimeCount++
			}
		case models.TripStatusCancelled:
			cancelledCount++
		}
	}
	return onTimeCount, completedCount, cancelledCount
}

func computeOnTimeRate(onTimeCount, completedCount int) float64 {
	if completedCount == 0 {
		return 0
	}
	return float64(onTimeCount) / float64(completedCount)
}

func rewardEarningType(reward models.Reward) (models.EarningType, bool) {
	switch reward.SKU {
	case "mealie_bag":
		return models.EarningTypeMealieBag, true
	case "household_item":
		return models.EarningTypeHouseholdItem, true
	case "weekly_gift":
		return models.EarningTypeWeeklyGift, true
	}
	if reward.Kind == "cash" && reward.AmountZMW > 0 {
		return models.EarningTypeB1DailyBonus, true
	}
	return "", false
}
