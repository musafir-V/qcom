package service

import (
	"testing"

	"github.com/qcom/qcom/internal/models"
)

func weeklyCfg() *models.PayoutConfig {
	return &models.PayoutConfig{
		WeeklyW1Days: 5, WeeklyW1BonusZMW: 150,
		WeeklyW2Days: 6, WeeklyW2BonusZMW: 250,
		WeeklyW3Days: 7, WeeklyW3BonusZMW: 400,
	}
}

func TestComputeWeeklyBonus_BelowThreshold(t *testing.T) {
	if computeWeeklyBonus(4, weeklyCfg()) != 0 {
		t.Fatal("expected 0 bonus for 4 days worked")
	}
}
func TestComputeWeeklyBonus_W1(t *testing.T) {
	if computeWeeklyBonus(5, weeklyCfg()) != 150 {
		t.Fatal("expected W1 bonus for 5 days")
	}
}
func TestComputeWeeklyBonus_W2(t *testing.T) {
	if computeWeeklyBonus(6, weeklyCfg()) != 250 {
		t.Fatal("expected W2 bonus for 6 days")
	}
}
func TestComputeWeeklyBonus_W3(t *testing.T) {
	if computeWeeklyBonus(7, weeklyCfg()) != 400 {
		t.Fatal("expected W3 bonus for 7 days")
	}
}
