package models

import "testing"

func TestEffectiveLimitZMW_Default(t *testing.T) {
	c := &CashConfig{}
	if got := c.EffectiveLimitZMW(); got != DefaultCashLimitZMW {
		t.Fatalf("expected default %v, got %v", DefaultCashLimitZMW, got)
	}
}

func TestEffectiveLimitZMW_Configured(t *testing.T) {
	c := &CashConfig{LimitZMW: 750}
	if got := c.EffectiveLimitZMW(); got != 750 {
		t.Fatalf("expected 750, got %v", got)
	}
}

func TestEffectiveLimitZMW_NonPositiveFallsBackToDefault(t *testing.T) {
	c := &CashConfig{LimitZMW: 0}
	if got := c.EffectiveLimitZMW(); got != DefaultCashLimitZMW {
		t.Fatalf("expected default for 0, got %v", got)
	}
}

func TestCashConfigKeys(t *testing.T) {
	c := &CashConfig{}
	if got := c.GetPK(); got != "CONFIG" {
		t.Fatalf("GetPK() = %q, want CONFIG", got)
	}
	if got := c.GetSK(); got != "CASH_V1" {
		t.Fatalf("GetSK() = %q, want CASH_V1", got)
	}
}
