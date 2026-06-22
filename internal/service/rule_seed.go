package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/qcom/qcom/internal/models"
)

type RuleSeedRepository interface {
	GetLatest(ctx context.Context, family models.RuleFamily, id string) (*models.Rule, error)
	Put(ctx context.Context, rule *models.Rule) error
}

// SeedDefaults inserts cofounder default rules for rate modifiers and bonuses.
// Idempotency: if any version already exists for a rule id, that rule is skipped.
func SeedDefaults(ctx context.Context, repo RuleSeedRepository) error {
	defaults, err := defaultRules()
	if err != nil {
		return err
	}

	for _, rule := range defaults {
		existing, err := repo.GetLatest(ctx, rule.Family, rule.ID)
		if err != nil {
			return fmt.Errorf("check existing rule %s/%s: %w", rule.Family, rule.ID, err)
		}
		if existing != nil {
			continue
		}
		if err := repo.Put(ctx, rule); err != nil {
			return fmt.Errorf("seed rule %s/%s: %w", rule.Family, rule.ID, err)
		}
	}
	return nil
}

func defaultRules() ([]*models.Rule, error) {
	rate := func(id, name string, days []int, start, end string, multiplier float64) (*models.Rule, error) {
		spec, err := json.Marshal(models.RateModifierSpec{
			DaysOfWeek: days,
			StartTime:  start,
			EndTime:    end,
			Multiplier: multiplier,
			FlatZMW:    0,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal rate spec for %s: %w", id, err)
		}
		return &models.Rule{
			ID:       id,
			Name:     name,
			Family:   models.FamilyRateModifier,
			Enabled:  true,
			Priority: 100,
			Version:  1,
			Spec:     spec,
		}, nil
	}

	accumulator := func(id, name string, threshold int, requireNoFail bool, minOnTimeRate float64, reward models.Reward) (*models.Rule, error) {
		spec, err := json.Marshal(models.AccumulatorSpec{
			Metric:        "on_time_trips",
			Window:        "weekly",
			Threshold:     threshold,
			RequireNoFail: requireNoFail,
			MinOnTimeRate: minOnTimeRate,
			Reward:        reward,
		})
		if id == "b1_daily_bonus" {
			var b1 models.AccumulatorSpec
			if err == nil {
				b1 = models.AccumulatorSpec{
					Metric:        "on_time_trips",
					Window:        "daily",
					Threshold:     threshold,
					RequireNoFail: requireNoFail,
					MinOnTimeRate: minOnTimeRate,
					Reward:        reward,
				}
				spec, err = json.Marshal(b1)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("marshal accumulator spec for %s: %w", id, err)
		}
		return &models.Rule{
			ID:       id,
			Name:     name,
			Family:   models.FamilyAccumulator,
			Enabled:  true,
			Priority: 100,
			Version:  1,
			Spec:     spec,
		}, nil
	}

	rankingSpec, err := json.Marshal(models.RankingSpec{
		Window:       "weekly",
		TopN:         3,
		MinOnTime:    1,
		WeightRate:   0.5,
		WeightVolume: 0.5,
		Reward: models.Reward{
			Kind:      "in_kind",
			AmountZMW: 0,
			Label:     "Weekly Gift",
			SKU:       "weekly_gift",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal ranking spec: %w", err)
	}

	rules := make([]*models.Rule, 0, 10)
	appendRule := func(r *models.Rule, err error) error {
		if err != nil {
			return err
		}
		rules = append(rules, r)
		return nil
	}

	if err := appendRule(rate("morning_peak", "Morning Peak", []int{}, "07:00", "08:30", 1.2)); err != nil {
		return nil, err
	}
	if err := appendRule(rate("night_peak", "Night Peak", []int{}, "19:30", "23:00", 1.3)); err != nil {
		return nil, err
	}
	if err := appendRule(rate("rush_peak", "Rush Peak", []int{}, "17:00", "19:00", 1.2)); err != nil {
		return nil, err
	}
	if err := appendRule(rate("friday_peak", "Friday Peak", []int{5}, "17:30", "23:00", 1.2)); err != nil {
		return nil, err
	}
	if err := appendRule(rate("saturday_peak", "Saturday Peak", []int{6}, "14:00", "23:00", 1.2)); err != nil {
		return nil, err
	}
	if err := appendRule(rate("sunday_peak", "Sunday Peak", []int{0}, "", "", 1.3)); err != nil {
		return nil, err
	}

	if err := appendRule(accumulator("b1_daily_bonus", "B1 Daily Bonus", 10, true, 0, models.Reward{
		Kind:      "cash",
		AmountZMW: 25,
		Label:     "",
		SKU:       "",
	})); err != nil {
		return nil, err
	}
	if err := appendRule(accumulator("b2_weekly_bonus", "B2 Weekly Mealie Bag", 80, false, 0.95, models.Reward{
		Kind:      "in_kind",
		AmountZMW: 0,
		Label:     "Mealie Bag",
		SKU:       "mealie_bag",
	})); err != nil {
		return nil, err
	}
	if err := appendRule(accumulator("b3_weekly_bonus", "B3 Weekly Household Item", 100, false, 0.95, models.Reward{
		Kind:      "in_kind",
		AmountZMW: 0,
		Label:     "Household Item",
		SKU:       "household_item",
	})); err != nil {
		return nil, err
	}

	rules = append(rules, &models.Rule{
		ID:       "b4_weekly_ranking",
		Name:     "B4 Weekly Ranking",
		Family:   models.FamilyRanking,
		Enabled:  true,
		Priority: 100,
		Version:  1,
		Spec:     rankingSpec,
	})

	return rules, nil
}
