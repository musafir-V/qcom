package models

import (
	"testing"
	"time"

	"github.com/qcom/qcom/internal/timezone"
)

func TestValidOperatingHours(t *testing.T) {
	tests := []struct {
		name     string
		opensAt  string
		closesAt string
		want     bool
	}{
		{"valid same-day window", "08:00", "22:00", true},
		{"missing opens_at", "", "22:00", false},
		{"missing closes_at", "08:00", "", false},
		{"invalid format", "8:00", "22:00", false},
		{"closes before opens", "22:00", "08:00", false},
		{"closes equals opens", "08:00", "08:00", false},
		{"overnight not allowed", "22:00", "02:00", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ds := Darkstore{OpensAt: tt.opensAt, ClosesAt: tt.closesAt}
			if got := ds.ValidOperatingHours(); got != tt.want {
				t.Fatalf("ValidOperatingHours() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsOperationalAt(t *testing.T) {
	loc := timezone.ZambiaLocation()
	morning := time.Date(2026, 6, 22, 9, 30, 0, 0, loc)
	beforeOpen := time.Date(2026, 6, 22, 7, 59, 0, 0, loc)
	afterClose := time.Date(2026, 6, 22, 22, 0, 0, 0, loc)

	ds := Darkstore{OpensAt: "08:00", ClosesAt: "22:00"}

	if !ds.IsOperationalAt(morning) {
		t.Fatal("expected store to be operational at 09:30")
	}
	if ds.IsOperationalAt(beforeOpen) {
		t.Fatal("expected store to be closed before opens_at")
	}
	if ds.IsOperationalAt(afterClose) {
		t.Fatal("expected store to be closed at closes_at (exclusive end)")
	}
}

func TestNextOpensAt(t *testing.T) {
	loc := timezone.ZambiaLocation()
	ds := Darkstore{OpensAt: "08:00", ClosesAt: "22:00"}

	beforeOpen := time.Date(2026, 6, 22, 7, 30, 0, 0, loc)
	next, ok := ds.NextOpensAt(beforeOpen)
	if !ok {
		t.Fatal("expected next opens_at")
	}
	if next != "2026-06-22T08:00:00+02:00" {
		t.Fatalf("expected opens later today, got %q", next)
	}

	afterClose := time.Date(2026, 6, 22, 22, 30, 0, 0, loc)
	next, ok = ds.NextOpensAt(afterClose)
	if !ok {
		t.Fatal("expected next opens_at")
	}
	if next != "2026-06-23T08:00:00+02:00" {
		t.Fatalf("expected opens tomorrow, got %q", next)
	}
}
