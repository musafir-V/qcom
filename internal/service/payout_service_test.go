package service

import (
	"testing"
	"time"

	"github.com/qcom/qcom/internal/models"
)

func TestComputeBasePay(t *testing.T) {
	cfg := &models.PayoutConfig{RatePerKmZMW: 5.0}
	if pay := computeBasePay(3.0, cfg); pay != 15.0 {
		t.Fatalf("expected 15.0 ZMW for 3km at 5/km, got %.2f", pay)
	}
}

func TestComputeBasePay_ZeroDistance(t *testing.T) {
	cfg := &models.PayoutConfig{RatePerKmZMW: 5.0}
	if pay := computeBasePay(0, cfg); pay != 0 {
		t.Fatalf("expected 0 ZMW for 0km, got %.2f", pay)
	}
}

func TestComputeBasePay_FractionalDistance(t *testing.T) {
	cfg := &models.PayoutConfig{RatePerKmZMW: 5.0}
	if pay := computeBasePay(2.5, cfg); pay != 12.5 {
		t.Fatalf("expected 12.5 ZMW for 2.5km at 5/km, got %.2f", pay)
	}
}

func TestComputeBasePay_FractionalPayout(t *testing.T) {
	cfg := &models.PayoutConfig{RatePerKmZMW: 5.0}
	if pay := computeBasePay(10.915, cfg); pay < 54.5749 || pay > 54.5751 {
		t.Fatalf("expected 54.575 ZMW for 10.915km at 5/km, got %.6f", pay)
	}
}

func TestComputeCompletionPayout_AppliesFrozenRateAndRounds(t *testing.T) {
	trip := &models.Trip{
		DistanceKM:     5.2,
		RateMultiplier: 1.2,
		RateFlatZMW:    0,
		SLAMinutes:     30,
		Tasks: []models.Task{
			{Type: models.TaskTypePickup, CompletedAt: "2026-06-22T10:00:00+02:00"},
			{Type: models.TaskTypeDrop, CompletedAt: "2026-06-22T10:20:00+02:00"},
		},
	}
	cfg := &models.PayoutConfig{RatePerKmZMW: 2.0}

	got := computeCompletionPayout(trip, cfg)
	if got.BasePayZMW != 10.4 {
		t.Fatalf("expected base 10.40, got %.2f", got.BasePayZMW)
	}
	if got.TotalPayZMW != 12.48 {
		t.Fatalf("expected total 12.48, got %.2f", got.TotalPayZMW)
	}
	if got.BonusPayZMW != 2.08 {
		t.Fatalf("expected surge bonus 2.08, got %.2f", got.BonusPayZMW)
	}
	if !got.OnTime {
		t.Fatal("expected trip to be on-time")
	}
}

func TestComputeCompletionPayout_RoundsOnlyFinalTotal(t *testing.T) {
	trip := &models.Trip{
		DistanceKM:     1.0, // raw base = 10.005 @ 10.005/km
		RateMultiplier: 1.5,
		RateFlatZMW:    0,
		SLAMinutes:     30,
	}
	cfg := &models.PayoutConfig{RatePerKmZMW: 10.005}

	got := computeCompletionPayout(trip, cfg)
	if got.BasePayZMW != 10.01 {
		t.Fatalf("expected display base 10.01, got %.2f", got.BasePayZMW)
	}
	if got.TotalPayZMW != 15.01 {
		t.Fatalf("expected single-rounded total 15.01, got %.2f", got.TotalPayZMW)
	}
	if got.BonusPayZMW != 5.00 {
		t.Fatalf("expected bonus 5.00 after single rounding, got %.2f", got.BonusPayZMW)
	}
}

func TestComputeCompletionPayout_DefaultMultiplierWhenFrozenZero(t *testing.T) {
	trip := &models.Trip{
		DistanceKM:     5.2,
		RateMultiplier: 0, // legacy/default persisted value
		RateFlatZMW:    0,
		SLAMinutes:     30,
	}
	cfg := &models.PayoutConfig{RatePerKmZMW: 2.0}

	got := computeCompletionPayout(trip, cfg)
	if got.TotalPayZMW != got.BasePayZMW {
		t.Fatalf("expected no surge when multiplier is zero/default, base=%.2f total=%.2f", got.BasePayZMW, got.TotalPayZMW)
	}
}

func TestComputeCompletionPayout_OnTimeFalseWhenLate(t *testing.T) {
	trip := &models.Trip{
		DistanceKM:     5.0,
		RateMultiplier: 1.0,
		SLAMinutes:     15,
		Tasks: []models.Task{
			{Type: models.TaskTypePickup, CompletedAt: "2026-06-22T10:00:00+02:00"},
			{Type: models.TaskTypeDrop, CompletedAt: "2026-06-22T10:20:00+02:00"},
		},
	}
	cfg := &models.PayoutConfig{RatePerKmZMW: 2.0}

	got := computeCompletionPayout(trip, cfg)
	if got.OnTime {
		t.Fatal("expected trip to be late")
	}
}

func TestComputeCompletionPayout_OnTimeFalseWhenActualDurationUnavailable(t *testing.T) {
	trip := &models.Trip{
		DistanceKM:     5.0,
		RateMultiplier: 1.0,
		SLAMinutes:     15,
		Tasks: []models.Task{
			{Type: models.TaskTypePickup, CompletedAt: ""},
			{Type: models.TaskTypeDrop, CompletedAt: time.Now().Format(time.RFC3339)},
		},
	}
	cfg := &models.PayoutConfig{RatePerKmZMW: 2.0}

	got := computeCompletionPayout(trip, cfg)
	if got.OnTime {
		t.Fatal("expected on_time=false when actual delivery duration is unavailable")
	}
}
