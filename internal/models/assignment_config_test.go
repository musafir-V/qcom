package models

import "testing"

func TestEffectiveAutoRejectSeconds_Default(t *testing.T) {
	c := &AssignmentConfig{}
	if got := c.EffectiveAutoRejectSeconds(); got != DefaultAutoRejectTimeSeconds {
		t.Fatalf("expected default %d, got %d", DefaultAutoRejectTimeSeconds, got)
	}
}

func TestEffectiveAutoRejectSeconds_Configured(t *testing.T) {
	c := &AssignmentConfig{AutoRejectTimeSeconds: 30}
	if got := c.EffectiveAutoRejectSeconds(); got != 30 {
		t.Fatalf("expected 30, got %d", got)
	}
}

func TestEffectiveAutoRejectSeconds_NonPositiveFallsBackToDefault(t *testing.T) {
	c := &AssignmentConfig{AutoRejectTimeSeconds: 0}
	if got := c.EffectiveAutoRejectSeconds(); got != DefaultAutoRejectTimeSeconds {
		t.Fatalf("expected default for 0, got %d", got)
	}
}
