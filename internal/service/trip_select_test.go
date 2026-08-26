package service

import (
	"testing"

	"github.com/qcom/qcom/internal/models"
)

func TestChooseTripForLegs_PrefersCompleted(t *testing.T) {
	cancelled := &models.Trip{OrderID: "ORD1", Status: models.TripStatusCancelled, CreatedAt: "2026-08-26T09:00:00Z", DistanceKM: 9}
	completed := &models.Trip{OrderID: "ORD1", Status: models.TripStatusCompleted, CreatedAt: "2026-08-26T08:00:00Z", DistanceKM: 2.4}
	got := ChooseTripForLegs([]*models.Trip{cancelled, completed})
	if got != completed {
		t.Fatalf("got %+v, want completed", got)
	}
}

func TestChooseTripForLegs_LatestNonCancelledWhenNoneCompleted(t *testing.T) {
	older := &models.Trip{OrderID: "ORD1", Status: models.TripStatusAssigned, CreatedAt: "2026-08-26T08:00:00Z"}
	newer := &models.Trip{OrderID: "ORD1", Status: models.TripStatusOutForDelivery, CreatedAt: "2026-08-26T09:00:00Z"}
	cancelled := &models.Trip{OrderID: "ORD1", Status: models.TripStatusCancelled, CreatedAt: "2026-08-26T10:00:00Z"}
	got := ChooseTripForLegs([]*models.Trip{older, newer, cancelled})
	if got != newer {
		t.Fatalf("got %+v, want newer OFD", got)
	}
}

func TestChooseTripForLegs_IgnoresDistanceFailedAndAllCancelled(t *testing.T) {
	if ChooseTripForLegs([]*models.Trip{
		{Status: models.TripStatusCancelled},
		{Status: models.TripStatusDistanceFailed},
	}) != nil {
		t.Fatal("expected nil")
	}
}

func TestChooseTripForLegs_Empty(t *testing.T) {
	if ChooseTripForLegs(nil) != nil {
		t.Fatal("expected nil")
	}
}
