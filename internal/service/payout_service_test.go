package service

import (
	"testing"

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

func TestComputeTierBonus_BelowTier1(t *testing.T) {
	cfg := &models.PayoutConfig{Tier1Threshold: 10, Tier1BonusZMW: 10, Tier2Threshold: 15, Tier2BonusZMW: 20}
	if bonus := computeTierBonus(5, cfg); bonus != 0 {
		t.Fatalf("expected 0 bonus for delivery 5, got %.2f", bonus)
	}
}

func TestComputeTierBonus_AtTier1(t *testing.T) {
	cfg := &models.PayoutConfig{Tier1Threshold: 10, Tier1BonusZMW: 10, Tier2Threshold: 15, Tier2BonusZMW: 20}
	if bonus := computeTierBonus(11, cfg); bonus != 10 {
		t.Fatalf("expected 10 ZMW tier1 bonus at delivery 11, got %.2f", bonus)
	}
}

func TestComputeTierBonus_AtTier2(t *testing.T) {
	cfg := &models.PayoutConfig{Tier1Threshold: 10, Tier1BonusZMW: 10, Tier2Threshold: 15, Tier2BonusZMW: 20}
	if bonus := computeTierBonus(16, cfg); bonus != 20 {
		t.Fatalf("expected 20 ZMW tier2 bonus at delivery 16, got %.2f", bonus)
	}
}

func TestComputeTierBonus_ExactTier1Boundary(t *testing.T) {
	cfg := &models.PayoutConfig{Tier1Threshold: 10, Tier1BonusZMW: 10, Tier2Threshold: 15, Tier2BonusZMW: 20}
	if computeTierBonus(10, cfg) != 0 {
		t.Fatal("delivery 10 should have no bonus (threshold is >10)")
	}
	if computeTierBonus(11, cfg) != 10 {
		t.Fatal("delivery 11 should earn tier1 bonus")
	}
}

func TestComputeTierBonus_ExactTier2Boundary(t *testing.T) {
	cfg := &models.PayoutConfig{Tier1Threshold: 10, Tier1BonusZMW: 10, Tier2Threshold: 15, Tier2BonusZMW: 20}
	if computeTierBonus(15, cfg) != 10 {
		t.Fatal("delivery 15 should earn tier1 bonus (tier2 threshold is >15)")
	}
	if computeTierBonus(16, cfg) != 20 {
		t.Fatal("delivery 16 should earn tier2 bonus")
	}
}

func TestComputeBasePay_FractionalDistance(t *testing.T) {
	cfg := &models.PayoutConfig{RatePerKmZMW: 5.0}
	if pay := computeBasePay(2.5, cfg); pay != 13 {
		t.Fatalf("expected 13 ZMW (rounded up) for 2.5km at 5/km, got %.2f", pay)
	}
}

func TestComputeBasePay_FractionalPayout(t *testing.T) {
	cfg := &models.PayoutConfig{RatePerKmZMW: 5.0}
	if pay := computeBasePay(10.915, cfg); pay != 55 {
		t.Fatalf("expected 55 ZMW (rounded up) for 10.915km at 5/km, got %.2f", pay)
	}
}

func TestComputeTierBonus_ZeroThresholds(t *testing.T) {
	cfg := &models.PayoutConfig{}
	if bonus := computeTierBonus(5, cfg); bonus != 0 {
		t.Fatalf("expected 0 bonus with zero config, got %.2f", bonus)
	}
}
