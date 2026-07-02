package models

import "testing"

func validDarkstoreForActivation() Darkstore {
	return Darkstore{
		Name:      "Test Store",
		Latitude:  12.97,
		Longitude: 77.64,
		Polygon: []PolygonPoint{
			{Lat: 12.96, Lng: 77.62},
			{Lat: 12.96, Lng: 77.66},
			{Lat: 12.99, Lng: 77.66},
		},
		OpensAt:  "07:00",
		ClosesAt: "23:00",
	}
}

func TestActivationBlockers_AllValid(t *testing.T) {
	d := validDarkstoreForActivation()
	if blockers := d.ActivationBlockers(); len(blockers) != 0 {
		t.Fatalf("expected no blockers, got %v", blockers)
	}
	if !d.ReadyForActivation() {
		t.Fatal("expected ReadyForActivation to be true")
	}
}

func TestActivationBlockers_MissingName(t *testing.T) {
	d := validDarkstoreForActivation()
	d.Name = "  "
	blockers := d.ActivationBlockers()
	if len(blockers) != 1 {
		t.Fatalf("expected 1 blocker, got %v", blockers)
	}
}

func TestActivationBlockers_ZeroLatLng(t *testing.T) {
	d := validDarkstoreForActivation()
	d.Latitude = 0
	d.Longitude = 0
	blockers := d.ActivationBlockers()
	if len(blockers) != 1 {
		t.Fatalf("expected 1 blocker, got %v", blockers)
	}
}

func TestActivationBlockers_TooFewPolygonPoints(t *testing.T) {
	d := validDarkstoreForActivation()
	d.Polygon = d.Polygon[:2]
	blockers := d.ActivationBlockers()
	if len(blockers) != 1 {
		t.Fatalf("expected 1 blocker, got %v", blockers)
	}
}

func TestActivationBlockers_EmptyPolygon(t *testing.T) {
	d := validDarkstoreForActivation()
	d.Polygon = nil
	blockers := d.ActivationBlockers()
	if len(blockers) != 1 {
		t.Fatalf("expected 1 blocker, got %v", blockers)
	}
}

func TestActivationBlockers_InvalidHours(t *testing.T) {
	d := validDarkstoreForActivation()
	d.OpensAt = "23:00"
	d.ClosesAt = "07:00"
	blockers := d.ActivationBlockers()
	if len(blockers) != 1 {
		t.Fatalf("expected 1 blocker, got %v", blockers)
	}
}

func TestActivationBlockers_MultipleAtOnce(t *testing.T) {
	d := Darkstore{}
	blockers := d.ActivationBlockers()
	if len(blockers) != 4 {
		t.Fatalf("expected 4 blockers for a fully empty darkstore, got %d: %v", len(blockers), blockers)
	}
}
