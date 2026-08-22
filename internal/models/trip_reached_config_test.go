package models

import "testing"

func TestEffectiveRadiusMeters_DefaultWhenZero(t *testing.T) {
	c := &TripReachedConfig{}
	if got := c.EffectiveRadiusMeters(); got != DefaultReachedRadiusMeters {
		t.Fatalf("got %v, want %v", got, DefaultReachedRadiusMeters)
	}
}

func TestEffectiveRadiusMeters_NegativeUsesDefault(t *testing.T) {
	c := &TripReachedConfig{RadiusMeters: -1}
	if got := c.EffectiveRadiusMeters(); got != DefaultReachedRadiusMeters {
		t.Fatalf("got %v, want %v", got, DefaultReachedRadiusMeters)
	}
}

func TestEffectiveRadiusMeters_Positive(t *testing.T) {
	c := &TripReachedConfig{RadiusMeters: 200}
	if got := c.EffectiveRadiusMeters(); got != 200 {
		t.Fatalf("got %v, want 200", got)
	}
}

func TestRequireReached_DefaultFalse(t *testing.T) {
	c := &TripReachedConfig{}
	if c.RequireReached() {
		t.Fatal("expected false")
	}
}

func TestRequireReached_True(t *testing.T) {
	c := &TripReachedConfig{RequireReachedBeforeComplete: true}
	if !c.RequireReached() {
		t.Fatal("expected true")
	}
}

func TestTripReachedConfigKeys(t *testing.T) {
	c := &TripReachedConfig{}
	if c.GetPK() != "CONFIG" || c.GetSK() != "TRIP_REACHED_V1" {
		t.Fatalf("keys = %s/%s", c.GetPK(), c.GetSK())
	}
}
