package models

import "testing"

func TestPayoutConfig_EffectiveMinutesPerKm_DefaultsWhenUnset(t *testing.T) {
	cfg := &PayoutConfig{}
	if got := cfg.EffectiveMinutesPerKm(); got != DefaultMinutesPerKm {
		t.Fatalf("expected default %.1f, got %.1f", DefaultMinutesPerKm, got)
	}
}

func TestPayoutConfig_EffectiveMinutesPerKm_UsesConfiguredValue(t *testing.T) {
	cfg := &PayoutConfig{MinutesPerKm: 3.5}
	if got := cfg.EffectiveMinutesPerKm(); got != 3.5 {
		t.Fatalf("expected configured value 3.5, got %.1f", got)
	}
}
