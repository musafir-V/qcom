package service

import (
	"strconv"
	"testing"

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
