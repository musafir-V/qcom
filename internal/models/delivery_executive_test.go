package models

import "testing"

func TestTripsToday(t *testing.T) {
	de := &DeliveryExecutive{DailyTripCount: 5, DailyCountDate: "2026-06-09"}

	if got := de.TripsToday("2026-06-09"); got != 5 {
		t.Fatalf("same day: expected 5, got %d", got)
	}
	if got := de.TripsToday("2026-06-10"); got != 0 {
		t.Fatalf("stale day: expected 0, got %d", got)
	}

	empty := &DeliveryExecutive{}
	if got := empty.TripsToday("2026-06-09"); got != 0 {
		t.Fatalf("never worked: expected 0, got %d", got)
	}
}
