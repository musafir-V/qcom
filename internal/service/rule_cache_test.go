package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type stubRuleCacheRepo struct {
	rules []*models.Rule
	err   error
}

func (s *stubRuleCacheRepo) ListAll(_ context.Context) ([]*models.Rule, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.rules, nil
}

func TestRuleCache_ActiveRateModifiersFiltersEnabledEffectiveAndLatest(t *testing.T) {
	mustSpec := func(multiplier float64) json.RawMessage {
		b, err := json.Marshal(models.RateModifierSpec{Multiplier: multiplier})
		if err != nil {
			t.Fatalf("marshal spec failed: %v", err)
		}
		return b
	}

	from := "2026-06-21T00:00:00+02:00"
	to := "2026-06-23T23:59:59+02:00"
	pastFrom := "2026-06-10T00:00:00+02:00"
	pastTo := "2026-06-15T23:59:59+02:00"

	repo := &stubRuleCacheRepo{
		rules: []*models.Rule{
			{
				ID:            "morning",
				Family:        models.FamilyRateModifier,
				Enabled:       true,
				EffectiveFrom: &from,
				EffectiveTo:   &to,
				Version:       1,
				Spec:          mustSpec(1.2),
			},
			{
				ID:            "morning",
				Family:        models.FamilyRateModifier,
				Enabled:       false,
				EffectiveFrom: &from,
				EffectiveTo:   &to,
				Version:       2, // latest disabled; should remove this id completely
				Spec:          mustSpec(1.3),
			},
			{
				ID:            "night",
				Family:        models.FamilyRateModifier,
				Enabled:       true,
				EffectiveFrom: &from,
				EffectiveTo:   &to,
				Version:       1,
				Spec:          mustSpec(1.3),
			},
			{
				ID:            "rush",
				Family:        models.FamilyRateModifier,
				Enabled:       true,
				EffectiveFrom: &pastFrom,
				EffectiveTo:   &pastTo,
				Version:       3,
				Spec:          mustSpec(1.2),
			},
			{
				ID:      "b1",
				Family:  models.FamilyAccumulator,
				Enabled: true,
				Version: 1,
				Spec:    json.RawMessage(`{"window":"daily"}`),
			},
		},
	}

	cache := newRuleCacheWithRepo(repo, 1*time.Minute, logrus.New())
	if err := cache.refresh(context.Background()); err != nil {
		t.Fatalf("refresh failed: %v", err)
	}

	at := time.Date(2026, 6, 22, 9, 0, 0, 0, mustLoadLusaka(t))
	got := cache.ActiveRateModifiers(at)

	if len(got) != 1 {
		t.Fatalf("len(active) = %d, want 1", len(got))
	}
	if got[0].ID != "night" || got[0].Version != 1 {
		t.Fatalf("active[0] = %+v, want night v1", *got[0])
	}
}

func mustLoadLusaka(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Africa/Lusaka")
	if err != nil {
		t.Fatalf("load location failed: %v", err)
	}
	return loc
}
