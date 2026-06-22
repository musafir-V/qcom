package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

func TestFareEngineResolveRate_OneMatchingRule(t *testing.T) {
	at := time.Date(2026, 6, 22, 18, 0, 0, 0, mustLoadLusaka(t))
	engine := newTestFareEngine(t, []*models.Rule{
		mustRateRule(t, "r-evening", 1, 10, models.RateModifierSpec{
			StartTime:  "17:00",
			EndTime:    "20:00",
			Multiplier: 1.3,
			FlatZMW:    2,
		}),
	})

	got := engine.ResolveRate(at, 50)
	if got.RuleID != "r-evening" || got.Version != 1 {
		t.Fatalf("resolved wrong rule: %+v", got)
	}
	if got.Multiplier != 1.3 || got.FlatZMW != 2 {
		t.Fatalf("resolved wrong pricing: %+v", got)
	}
}

func TestFareEngineResolveRate_PicksHigherPayingRule(t *testing.T) {
	at := time.Date(2026, 6, 22, 18, 0, 0, 0, mustLoadLusaka(t))
	engine := newTestFareEngine(t, []*models.Rule{
		mustRateRule(t, "mult-130", 1, 1, models.RateModifierSpec{Multiplier: 1.3}),
		mustRateRule(t, "flat-10", 1, 1, models.RateModifierSpec{FlatZMW: 10}),
	})

	got := engine.ResolveRate(at, 50)
	if got.RuleID != "mult-130" {
		t.Fatalf("expected multiplier rule to win on base 50, got %+v", got)
	}
	if paid := ApplyRate(50, got); paid != 65 {
		t.Fatalf("expected payout 65.00, got %.2f", paid)
	}
}

func TestFareEngineResolveRate_TieBreakPriorityThenID(t *testing.T) {
	at := time.Date(2026, 6, 22, 18, 0, 0, 0, mustLoadLusaka(t))
	engine := newTestFareEngine(t, []*models.Rule{
		mustRateRule(t, "z-lower-priority", 1, 1, models.RateModifierSpec{FlatZMW: 10}),
		mustRateRule(t, "m-higher-priority", 1, 2, models.RateModifierSpec{FlatZMW: 10}),
		mustRateRule(t, "a-higher-priority", 1, 2, models.RateModifierSpec{FlatZMW: 10}),
	})

	got := engine.ResolveRate(at, 50)
	if got.RuleID != "a-higher-priority" {
		t.Fatalf("expected highest priority then lexicographically smaller id, got %+v", got)
	}
}

func TestFareEngineResolveRate_RespectsTimeAndDayWindow(t *testing.T) {
	loc := mustLoadLusaka(t)
	saturday := time.Date(2026, 6, 20, 18, 0, 0, 0, loc)
	sunday := time.Date(2026, 6, 21, 18, 0, 0, 0, loc)
	saturdayOutsideTime := time.Date(2026, 6, 20, 16, 59, 0, 0, loc)
	engine := newTestFareEngine(t, []*models.Rule{
		mustRateRule(t, "sat-evening", 1, 10, models.RateModifierSpec{
			DaysOfWeek: []int{int(time.Saturday)},
			StartTime:  "17:00",
			EndTime:    "21:00",
			FlatZMW:    10,
		}),
	})

	if got := engine.ResolveRate(saturday, 50); got.RuleID != "sat-evening" {
		t.Fatalf("expected saturday evening to match, got %+v", got)
	}
	if got := engine.ResolveRate(saturdayOutsideTime, 50); got.RuleID != "" || got.Multiplier != 1 || got.FlatZMW != 0 {
		t.Fatalf("expected outside time window to fall back to default, got %+v", got)
	}
	if got := engine.ResolveRate(sunday, 50); got.RuleID != "" || got.Multiplier != 1 || got.FlatZMW != 0 {
		t.Fatalf("expected outside day window to fall back to default, got %+v", got)
	}
}

func TestFareEngineResolveRate_NoneMatchReturnsDefault(t *testing.T) {
	at := time.Date(2026, 6, 22, 10, 0, 0, 0, mustLoadLusaka(t))
	engine := newTestFareEngine(t, []*models.Rule{
		mustRateRule(t, "weekday-only", 1, 1, models.RateModifierSpec{
			DaysOfWeek: []int{int(time.Sunday)},
			FlatZMW:    10,
		}),
	})

	got := engine.ResolveRate(at, 50)
	if got.RuleID != "" || got.Version != 0 || got.Multiplier != 1 || got.FlatZMW != 0 {
		t.Fatalf("expected default decision, got %+v", got)
	}
	if paid := ApplyRate(12.48, got); paid != 12.48 {
		t.Fatalf("expected ApplyRate to preserve rounded base, got %.2f", paid)
	}
}

func TestApplyRate_RoundsToTwoDecimals(t *testing.T) {
	decision := RateDecision{Multiplier: 1.2, FlatZMW: 0}
	if got := ApplyRate(10.4, decision); got != 12.48 {
		t.Fatalf("expected 12.48, got %.2f", got)
	}
}

func newTestFareEngine(t *testing.T, rules []*models.Rule) *FareEngine {
	t.Helper()
	cache := newRuleCacheWithRepo(&stubRuleCacheRepo{rules: rules}, time.Minute, logrus.New())
	if err := cache.refresh(context.Background()); err != nil {
		t.Fatalf("refresh rule cache: %v", err)
	}
	return NewFareEngine(cache)
}

func mustRateRule(t *testing.T, id string, version, priority int, spec models.RateModifierSpec) *models.Rule {
	t.Helper()
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	return &models.Rule{
		ID:       id,
		Family:   models.FamilyRateModifier,
		Enabled:  true,
		Priority: priority,
		Version:  version,
		Spec:     b,
	}
}
