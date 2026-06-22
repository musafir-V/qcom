package service

import (
	"testing"
	"time"

	"github.com/qcom/qcom/internal/models"
)

func TestCanCall(t *testing.T) {
	base := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		status models.TripStatus
		done   string
		now    time.Time
		want   bool
	}{
		{"ofd", models.TripStatusOutForDelivery, "", base, true},
		{"accepted blocked", models.TripStatusAccepted, "", base, false},
		{"cancelled blocked", models.TripStatusCancelled, "", base, false},
		{"in grace", models.TripStatusCompleted, base.Format(time.RFC3339), base.Add(30 * time.Minute), true},
		{"grace expired", models.TripStatusCompleted, base.Format(time.RFC3339), base.Add(90 * time.Minute), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			trip := &models.Trip{Status: c.status, CompletedAt: c.done}
			got, _ := CanCall(trip, c.now)
			if got != c.want {
				t.Fatalf("CanCall = %v, want %v", got, c.want)
			}
		})
	}
}
