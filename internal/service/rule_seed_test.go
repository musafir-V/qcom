package service

import (
	"context"
	"testing"

	"github.com/qcom/qcom/internal/models"
)

type stubRuleSeedRepo struct {
	existing map[string]*models.Rule
	putCalls []*models.Rule
}

func (s *stubRuleSeedRepo) GetLatest(_ context.Context, family models.RuleFamily, id string) (*models.Rule, error) {
	if s.existing == nil {
		return nil, nil
	}
	return s.existing[string(family)+"#"+id], nil
}

func (s *stubRuleSeedRepo) Put(_ context.Context, rule *models.Rule) error {
	s.putCalls = append(s.putCalls, rule)
	return nil
}

func TestSeedDefaults_SkipsExistingRuleID(t *testing.T) {
	repo := &stubRuleSeedRepo{
		existing: map[string]*models.Rule{
			"rate_modifier#morning_peak": {
				ID:      "morning_peak",
				Family:  models.FamilyRateModifier,
				Version: 2,
			},
		},
	}

	if err := SeedDefaults(context.Background(), repo); err != nil {
		t.Fatalf("SeedDefaults failed: %v", err)
	}

	for _, put := range repo.putCalls {
		if put.Family == models.FamilyRateModifier && put.ID == "morning_peak" {
			t.Fatalf("expected morning_peak to be skipped when any version exists")
		}
	}
	if len(repo.putCalls) == 0 {
		t.Fatalf("expected at least one default rule to be written")
	}
}

func TestSeedDefaults_WritesAllWhenNoneExist(t *testing.T) {
	repo := &stubRuleSeedRepo{}

	if err := SeedDefaults(context.Background(), repo); err != nil {
		t.Fatalf("SeedDefaults failed: %v", err)
	}

	if got, want := len(repo.putCalls), 10; got != want {
		t.Fatalf("put calls = %d, want %d", got, want)
	}
}
