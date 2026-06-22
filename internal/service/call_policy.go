package service

import (
	"time"

	"github.com/qcom/qcom/internal/models"
)

const CallGracePeriod = 60 * time.Minute

func CanCall(trip *models.Trip, now time.Time) (bool, string) {
	switch trip.Status {
	case models.TripStatusOutForDelivery:
		return true, ""
	case models.TripStatusCompleted:
		done, err := time.Parse(time.RFC3339, trip.CompletedAt)
		if err != nil {
			return false, "trip_not_callable"
		}
		if now.Sub(done) <= CallGracePeriod {
			return true, ""
		}
		return false, "grace_expired"
	default:
		return false, "trip_not_callable"
	}
}
