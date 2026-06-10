package service

import (
	"strconv"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/models"
)

func TestSortTripsByCreatedAt_OldestFirst(t *testing.T) {
	trips := []*models.Trip{
		{TripID: "c", CreatedAt: "2026-06-02T10:00:02Z"},
		{TripID: "a", CreatedAt: "2026-06-02T10:00:00Z"},
		{TripID: "b", CreatedAt: "2026-06-02T10:00:01Z"},
	}

	sortTripsByCreatedAt(trips)

	got := []string{trips[0].TripID, trips[1].TripID, trips[2].TripID}
	want := []string{"a", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("FIFO order wrong: got %v, want %v", got, want)
		}
	}
}

func TestSortTripsByCreatedAt_StableForEqualTimestamps(t *testing.T) {
	trips := []*models.Trip{
		{TripID: "first", CreatedAt: "2026-06-02T10:00:00Z"},
		{TripID: "second", CreatedAt: "2026-06-02T10:00:00Z"},
	}

	sortTripsByCreatedAt(trips)

	if trips[0].TripID != "first" || trips[1].TripID != "second" {
		t.Fatalf("equal timestamps should preserve input order, got %s,%s", trips[0].TripID, trips[1].TripID)
	}
}

func TestIsAcceptExpired(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

	expired := &models.Trip{
		Status:         models.TripStatusAssigned,
		AcceptDeadline: now.Add(-1 * time.Second).Format(time.RFC3339),
	}
	if !isAcceptExpired(expired, now) {
		t.Error("expected expired assigned trip to be auto-rejectable")
	}

	future := &models.Trip{
		Status:         models.TripStatusAssigned,
		AcceptDeadline: now.Add(30 * time.Second).Format(time.RFC3339),
	}
	if isAcceptExpired(future, now) {
		t.Error("expected future-deadline trip to NOT be expired")
	}

	accepted := &models.Trip{
		Status:         models.TripStatusAccepted,
		AcceptDeadline: now.Add(-1 * time.Second).Format(time.RFC3339),
	}
	if isAcceptExpired(accepted, now) {
		t.Error("expected accepted trip to never be auto-rejected")
	}

	noDeadline := &models.Trip{Status: models.TripStatusAssigned}
	if isAcceptExpired(noDeadline, now) {
		t.Error("expected trip with no deadline to NOT be expired")
	}
}

func TestRandomOTP_FourNumericDigitsInRange(t *testing.T) {
	for i := 0; i < 1000; i++ {
		otp := randomOTP()
		if len(otp) != 4 {
			t.Fatalf("otp %q is not 4 characters", otp)
		}
		n, err := strconv.Atoi(otp)
		if err != nil {
			t.Fatalf("otp %q is not numeric: %v", otp, err)
		}
		if n < 1000 || n > 9999 {
			t.Fatalf("otp %d out of range [1000,9999]", n)
		}
	}
}
