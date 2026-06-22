package service

import (
	"testing"
	"time"

	"github.com/qcom/qcom/internal/models"
)

func TestEvaluateAccumulator_B1DailyBonusAwarded(t *testing.T) {
	spec := models.AccumulatorSpec{
		Metric:        "on_time_trips",
		Window:        "daily",
		Threshold:     10,
		RequireNoFail: true,
		Reward: models.Reward{
			Kind:      "cash",
			AmountZMW: 25,
		},
	}

	trips := make([]*models.Trip, 0, 10)
	for i := 0; i < 10; i++ {
		trips = append(trips, completedTrip(true, "2026-06-22T10:00:00+02:00"))
	}

	got := EvaluateAccumulator("de-1", spec, "2026-06-22", trips)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	entry := got[0]
	if entry.DEID != "de-1" {
		t.Fatalf("DEID = %q, want de-1", entry.DEID)
	}
	if entry.Type != models.EarningTypeB1DailyBonus {
		t.Fatalf("Type = %q, want %q", entry.Type, models.EarningTypeB1DailyBonus)
	}
	if entry.AmountZMW != 25 {
		t.Fatalf("AmountZMW = %.2f, want 25", entry.AmountZMW)
	}
	if entry.ReferenceID != "2026-06-22" {
		t.Fatalf("ReferenceID = %q, want 2026-06-22", entry.ReferenceID)
	}
}

func TestEvaluateAccumulator_B1RequireNoFailBlocksOnCancellation(t *testing.T) {
	spec := models.AccumulatorSpec{
		Metric:        "on_time_trips",
		Window:        "daily",
		Threshold:     10,
		RequireNoFail: true,
		Reward: models.Reward{
			Kind:      "cash",
			AmountZMW: 25,
		},
	}

	trips := make([]*models.Trip, 0, 11)
	for i := 0; i < 10; i++ {
		trips = append(trips, completedTrip(true, "2026-06-22T10:00:00+02:00"))
	}
	trips = append(trips, cancelledTrip("2026-06-22T11:00:00+02:00"))

	got := EvaluateAccumulator("de-1", spec, "2026-06-22", trips)
	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0", len(got))
	}
}

func TestEvaluateAccumulator_B2RequiresThresholdAndRate(t *testing.T) {
	spec := models.AccumulatorSpec{
		Metric:        "on_time_trips",
		Window:        "weekly",
		Threshold:     80,
		MinOnTimeRate: 0.95,
		Reward: models.Reward{
			Kind:      "in_kind",
			AmountZMW: 0,
			Label:     "Mealie Bag",
			SKU:       "mealie_bag",
		},
	}

	t.Run("awarded_when_threshold_and_rate_met", func(t *testing.T) {
		trips := make([]*models.Trip, 0, 84)
		for i := 0; i < 80; i++ {
			trips = append(trips, completedTrip(true, "2026-06-22T10:00:00+02:00"))
		}
		for i := 0; i < 4; i++ {
			trips = append(trips, completedTrip(false, "2026-06-22T11:00:00+02:00"))
		}
		// Cancelled trips are treated as failures for "no fail" logic but are not
		// denominator-eligible completed trips for on-time rate.
		trips = append(trips, cancelledTrip("2026-06-22T12:00:00+02:00"))

		got := EvaluateAccumulator("de-2", spec, "2026-W26", trips)
		if len(got) != 1 {
			t.Fatalf("len(got) = %d, want 1", len(got))
		}
		entry := got[0]
		if entry.Type != models.EarningTypeMealieBag {
			t.Fatalf("Type = %q, want %q", entry.Type, models.EarningTypeMealieBag)
		}
		if entry.AmountZMW != 0 {
			t.Fatalf("AmountZMW = %.2f, want 0", entry.AmountZMW)
		}
		if entry.Label != "Mealie Bag" {
			t.Fatalf("Label = %q, want %q", entry.Label, "Mealie Bag")
		}
		if entry.ReferenceID != "2026-W26" {
			t.Fatalf("ReferenceID = %q, want 2026-W26", entry.ReferenceID)
		}
	})

	t.Run("not_awarded_when_rate_below_minimum", func(t *testing.T) {
		trips := make([]*models.Trip, 0, 85)
		for i := 0; i < 80; i++ {
			trips = append(trips, completedTrip(true, "2026-06-22T10:00:00+02:00"))
		}
		for i := 0; i < 5; i++ {
			trips = append(trips, completedTrip(false, "2026-06-22T11:00:00+02:00"))
		}

		got := EvaluateAccumulator("de-2", spec, "2026-W26", trips)
		if len(got) != 0 {
			t.Fatalf("len(got) = %d, want 0", len(got))
		}
	})
}

func TestEvaluateAccumulator_B2AndB3Independent(t *testing.T) {
	b2 := models.AccumulatorSpec{
		Metric:        "on_time_trips",
		Window:        "weekly",
		Threshold:     80,
		MinOnTimeRate: 0.95,
		Reward: models.Reward{
			Kind:      "in_kind",
			AmountZMW: 0,
			Label:     "Mealie Bag",
			SKU:       "mealie_bag",
		},
	}
	b3 := models.AccumulatorSpec{
		Metric:        "on_time_trips",
		Window:        "weekly",
		Threshold:     100,
		MinOnTimeRate: 0.95,
		Reward: models.Reward{
			Kind:      "in_kind",
			AmountZMW: 0,
			Label:     "Household Item",
			SKU:       "household_item",
		},
	}

	trips := make([]*models.Trip, 0, 105)
	for i := 0; i < 100; i++ {
		trips = append(trips, completedTrip(true, "2026-06-22T10:00:00+02:00"))
	}
	for i := 0; i < 5; i++ {
		trips = append(trips, completedTrip(false, "2026-06-22T11:00:00+02:00"))
	}

	gotB2 := EvaluateAccumulator("de-3", b2, "2026-W26", trips)
	gotB3 := EvaluateAccumulator("de-3", b3, "2026-W26", trips)
	if len(gotB2) != 1 || len(gotB3) != 1 {
		t.Fatalf("expected both B2 and B3 to fire independently, got b2=%d b3=%d", len(gotB2), len(gotB3))
	}
	if gotB2[0].Type != models.EarningTypeMealieBag {
		t.Fatalf("B2 type = %q, want %q", gotB2[0].Type, models.EarningTypeMealieBag)
	}
	if gotB3[0].Type != models.EarningTypeHouseholdItem {
		t.Fatalf("B3 type = %q, want %q", gotB3[0].Type, models.EarningTypeHouseholdItem)
	}
}

func TestEvaluateRanking_WeeklyTopNByScore(t *testing.T) {
	spec := models.RankingSpec{
		Window:       "weekly",
		TopN:         3,
		MinOnTime:    2,
		WeightRate:   0.5,
		WeightVolume: 0.5,
		Reward: models.Reward{
			Kind:      "in_kind",
			AmountZMW: 0,
			Label:     "Weekly Gift",
			SKU:       "weekly_gift",
		},
	}

	perDE := map[string][]*models.Trip{
		"de-a": makeOnTimeTrips(10, "2026-06-16T08:00:00+02:00"),
		"de-b": makeOnTimeTrips(9, "2026-06-16T08:00:00+02:00"),
		"de-c": makeOnTimeTrips(8, "2026-06-16T08:00:00+02:00"),
		"de-d": makeOnTimeTrips(7, "2026-06-16T08:00:00+02:00"),
	}

	got := EvaluateRanking(spec, "2026-W26", perDE)
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	assertContainsDEs(t, got, []string{"de-a", "de-b", "de-c"})
	for _, entry := range got {
		if entry.Type != models.EarningTypeWeeklyGift {
			t.Fatalf("Type = %q, want %q", entry.Type, models.EarningTypeWeeklyGift)
		}
		if entry.AmountZMW != 0 {
			t.Fatalf("AmountZMW = %.2f, want 0", entry.AmountZMW)
		}
		if entry.Label != "Weekly Gift" {
			t.Fatalf("Label = %q, want Weekly Gift", entry.Label)
		}
		if entry.ReferenceID != "2026-W26" {
			t.Fatalf("ReferenceID = %q, want 2026-W26", entry.ReferenceID)
		}
	}
}

func TestEvaluateRanking_ExcludesBelowMinOnTimeFloor(t *testing.T) {
	spec := models.RankingSpec{
		Window:       "weekly",
		TopN:         3,
		MinOnTime:    5,
		WeightRate:   0.5,
		WeightVolume: 0.5,
		Reward: models.Reward{
			Kind:      "in_kind",
			AmountZMW: 0,
			Label:     "Weekly Gift",
			SKU:       "weekly_gift",
		},
	}
	perDE := map[string][]*models.Trip{
		"eligible": makeOnTimeTrips(5, "2026-06-16T08:00:00+02:00"),
		"low":      makeOnTimeTrips(4, "2026-06-16T08:00:00+02:00"),
	}

	got := EvaluateRanking(spec, "2026-W26", perDE)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].DEID != "eligible" {
		t.Fatalf("winner = %q, want eligible", got[0].DEID)
	}
}

func TestEvaluateRanking_TieBreakByOnTimeCountAtBoundary(t *testing.T) {
	spec := models.RankingSpec{
		Window:       "weekly",
		TopN:         3,
		MinOnTime:    1,
		WeightRate:   1,
		WeightVolume: 0,
		Reward: models.Reward{
			Kind:      "in_kind",
			AmountZMW: 0,
			Label:     "Weekly Gift",
			SKU:       "weekly_gift",
		},
	}

	perDE := map[string][]*models.Trip{
		"de-10": makeOnTimeTrips(10, "2026-06-16T08:00:00+02:00"),
		"de-9":  makeOnTimeTrips(9, "2026-06-16T08:00:00+02:00"),
		"de-8":  makeOnTimeTrips(8, "2026-06-16T08:00:00+02:00"),
		"de-7":  makeOnTimeTrips(7, "2026-06-16T08:00:00+02:00"),
	}

	got := EvaluateRanking(spec, "2026-W26", perDE)
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	assertContainsDEs(t, got, []string{"de-10", "de-9", "de-8"})
	for _, entry := range got {
		if entry.DEID == "de-7" {
			t.Fatalf("de-7 should lose tie-break to higher on-time count")
		}
	}
}

func TestEvaluateRanking_TieBreakByEarliestReachThenDEID(t *testing.T) {
	spec := models.RankingSpec{
		Window:       "weekly",
		TopN:         1,
		MinOnTime:    1,
		WeightRate:   0.5,
		WeightVolume: 0.5,
		Reward: models.Reward{
			Kind:      "in_kind",
			AmountZMW: 0,
			Label:     "Weekly Gift",
			SKU:       "weekly_gift",
		},
	}

	early := makeOnTimeTrips(8, "2026-06-16T08:00:00+02:00")
	late := makeOnTimeTrips(8, "2026-06-16T09:00:00+02:00")
	perDE := map[string][]*models.Trip{
		"de-early": early,
		"de-late":  late,
	}
	got := EvaluateRanking(spec, "2026-W26", perDE)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].DEID != "de-early" {
		t.Fatalf("winner = %q, want de-early", got[0].DEID)
	}

	// Equal score, equal on-time count, equal reach time => deterministic by DEID.
	perDE = map[string][]*models.Trip{
		"de-a": makeOnTimeTrips(8, "2026-06-16T08:00:00+02:00"),
		"de-b": makeOnTimeTrips(8, "2026-06-16T08:00:00+02:00"),
	}
	got = EvaluateRanking(spec, "2026-W26", perDE)
	if got[0].DEID != "de-a" {
		t.Fatalf("winner = %q, want de-a for deterministic DEID tie-break", got[0].DEID)
	}
}

func completedTrip(onTime bool, completedAt string) *models.Trip {
	return &models.Trip{
		Status:      models.TripStatusCompleted,
		OnTime:      onTime,
		CompletedAt: completedAt,
	}
}

func cancelledTrip(cancelledAt string) *models.Trip {
	return &models.Trip{
		Status:      models.TripStatusCancelled,
		CancelledAt: cancelledAt,
	}
}

func makeOnTimeTrips(n int, firstCompletedAt string) []*models.Trip {
	out := make([]*models.Trip, 0, n)
	base := mustParseRFC3339(firstCompletedAt)
	for i := 0; i < n; i++ {
		out = append(out, completedTrip(true, base.Add(time.Duration(i)*time.Minute).Format(time.RFC3339)))
	}
	return out
}

func assertContainsDEs(t *testing.T, entries []*models.EarningsLedger, want []string) {
	t.Helper()
	have := make(map[string]bool, len(entries))
	for _, e := range entries {
		have[e.DEID] = true
	}
	for _, id := range want {
		if !have[id] {
			t.Fatalf("missing winner %q in %+v", id, entries)
		}
	}
}

func mustParseRFC3339(v string) time.Time {
	parsed, err := time.Parse(time.RFC3339, v)
	if err != nil {
		panic(err)
	}
	return parsed
}
