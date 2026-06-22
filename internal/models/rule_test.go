package models

import (
	"encoding/json"
	"testing"
)

func TestRuleKeys(t *testing.T) {
	r := &Rule{
		ID:      "morning_peak",
		Family:  FamilyRateModifier,
		Version: 3,
	}

	if got, want := r.GetPK(), "RULE"; got != want {
		t.Errorf("GetPK() = %q, want %q", got, want)
	}
	if got, want := r.GetSK(), "rate_modifier#morning_peak#v3"; got != want {
		t.Errorf("GetSK() = %q, want %q", got, want)
	}
}

func TestRateModifierSpecJSONRoundTrip(t *testing.T) {
	original := RateModifierSpec{
		DaysOfWeek: []int{1, 2, 3, 4, 5},
		StartTime:  "17:30",
		EndTime:    "23:00",
		Multiplier: 1.2,
		FlatZMW:    3.5,
	}

	var got RateModifierSpec
	roundTripSpec(t, original, &got)

	if got.StartTime != original.StartTime || got.EndTime != original.EndTime ||
		got.Multiplier != original.Multiplier || got.FlatZMW != original.FlatZMW ||
		len(got.DaysOfWeek) != len(original.DaysOfWeek) {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, original)
	}
	for i := range original.DaysOfWeek {
		if got.DaysOfWeek[i] != original.DaysOfWeek[i] {
			t.Fatalf("days_of_week[%d] = %d, want %d", i, got.DaysOfWeek[i], original.DaysOfWeek[i])
		}
	}
}

func TestAccumulatorSpecJSONRoundTrip(t *testing.T) {
	original := AccumulatorSpec{
		Metric:        "on_time_trips",
		Window:        "weekly",
		Threshold:     80,
		RequireNoFail: true,
		MinOnTimeRate: 0.95,
		Reward: Reward{
			Kind:      "in_kind",
			AmountZMW: 0,
			Label:     "Mealie Bag",
			SKU:       "mealie_bag",
		},
	}

	var got AccumulatorSpec
	roundTripSpec(t, original, &got)

	if got != original {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, original)
	}
}

func TestRankingSpecJSONRoundTrip(t *testing.T) {
	original := RankingSpec{
		Window:       "weekly",
		TopN:         3,
		MinOnTime:    40,
		WeightRate:   0.5,
		WeightVolume: 0.5,
		Reward: Reward{
			Kind:      "in_kind",
			AmountZMW: 0,
			Label:     "Weekly Gift",
			SKU:       "weekly_gift",
		},
	}

	var got RankingSpec
	roundTripSpec(t, original, &got)

	if got != original {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, original)
	}
}

func roundTripSpec[T any](t *testing.T, in T, out *T) {
	t.Helper()

	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
}
