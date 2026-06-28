package service

import (
	"sort"
	"time"

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
			Type:        earningType,
			AmountZMW:   spec.Reward.AmountZMW,
			Label:       spec.Reward.Label,
			CreatedAt:   time.Now().UTC().Format(time.RFC3339),
			ReferenceID: window,
		},
	}
}

func EvaluateRanking(spec models.RankingSpec, window string, perDE map[string][]*models.Trip) []*models.EarningsLedger {
	if spec.TopN <= 0 {
		return nil
	}
	earningType, ok := rewardEarningType(spec.Reward)
	if !ok {
		return nil
	}

	type candidate struct {
		deID          string
		onTimeCount   int
		completed     int
		onTimeRate    float64
		normalizedVol float64
		score         float64
		reachedAt     time.Time
	}

	candidates := make([]candidate, 0, len(perDE))
	maxOnTimeCount := 0
	for deID, trips := range perDE {
		onTimeCount, completedCount, _ := summarizeTrips(trips)
		if onTimeCount < spec.MinOnTime {
			continue
		}

		reachedAt := reachedAtForOnTimeCount(trips, onTimeCount)
		candidates = append(candidates, candidate{
			deID:        deID,
			onTimeCount: onTimeCount,
			completed:   completedCount,
			onTimeRate:  computeOnTimeRate(onTimeCount, completedCount),
			reachedAt:   reachedAt,
		})
		if onTimeCount > maxOnTimeCount {
			maxOnTimeCount = onTimeCount
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	for i := range candidates {
		if maxOnTimeCount > 0 {
			candidates[i].normalizedVol = float64(candidates[i].onTimeCount) / float64(maxOnTimeCount)
		}
		candidates[i].score = spec.WeightRate*candidates[i].onTimeRate + spec.WeightVolume*candidates[i].normalizedVol
	}

	sort.Slice(candidates, func(i, j int) bool {
		a := candidates[i]
		b := candidates[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.onTimeCount != b.onTimeCount {
			return a.onTimeCount > b.onTimeCount
		}
		if !a.reachedAt.Equal(b.reachedAt) {
			return a.reachedAt.Before(b.reachedAt)
		}
		return a.deID < b.deID
	})

	limit := spec.TopN
	if limit > len(candidates) {
		limit = len(candidates)
	}
	out := make([]*models.EarningsLedger, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, &models.EarningsLedger{
			DEID:        candidates[i].deID,
			Type:        earningType,
			AmountZMW:   spec.Reward.AmountZMW,
			Label:       spec.Reward.Label,
			CreatedAt:   time.Now().UTC().Format(time.RFC3339),
			ReferenceID: window,
		})
	}
	return out
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

func reachedAtForOnTimeCount(trips []*models.Trip, target int) time.Time {
	if target <= 0 {
		return farFutureTime()
	}
	type timestampedTrip struct {
		completedAt time.Time
	}
	onTimeTrips := make([]timestampedTrip, 0, len(trips))
	for _, trip := range trips {
		if trip == nil || trip.Status != models.TripStatusCompleted || !trip.OnTime || trip.CompletedAt == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, trip.CompletedAt)
		if err != nil {
			continue
		}
		onTimeTrips = append(onTimeTrips, timestampedTrip{completedAt: parsed})
	}
	if len(onTimeTrips) < target {
		return farFutureTime()
	}
	sort.Slice(onTimeTrips, func(i, j int) bool {
		return onTimeTrips[i].completedAt.Before(onTimeTrips[j].completedAt)
	})
	return onTimeTrips[target-1].completedAt
}

func farFutureTime() time.Time {
	return time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC)
}
