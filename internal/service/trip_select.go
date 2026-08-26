package service

import (
	"time"

	"github.com/qcom/qcom/internal/models"
)

// ChooseTripForLegs picks the trip used for day-leg km and drop reached_at:
// a completed trip (latest CompletedAt, else CreatedAt), otherwise the latest
// non-cancelled / non-distance_failed trip by CreatedAt. Nil if none usable.
func ChooseTripForLegs(trips []*models.Trip) *models.Trip {
	var completed []*models.Trip
	var others []*models.Trip
	for _, t := range trips {
		if t == nil {
			continue
		}
		switch t.Status {
		case models.TripStatusCancelled, models.TripStatusDistanceFailed:
			continue
		case models.TripStatusCompleted:
			completed = append(completed, t)
		default:
			others = append(others, t)
		}
	}
	if len(completed) > 0 {
		return latestTrip(completed, completedSortKey)
	}
	return latestTrip(others, func(t *models.Trip) string { return t.CreatedAt })
}

func completedSortKey(t *models.Trip) string {
	if t.CompletedAt != "" {
		return t.CompletedAt
	}
	return t.CreatedAt
}

// latestTrip returns the trip with the latest RFC3339 key. Unparseable keys
// sort last; equal timestamps keep the first-seen trip.
func latestTrip(trips []*models.Trip, key func(*models.Trip) string) *models.Trip {
	if len(trips) == 0 {
		return nil
	}
	best := trips[0]
	bestTime, bestOK := parseRFC3339(key(best))
	for _, t := range trips[1:] {
		ts, ok := parseRFC3339(key(t))
		if !ok {
			continue
		}
		if !bestOK || ts.After(bestTime) {
			best = t
			bestTime = ts
			bestOK = true
		}
	}
	return best
}

func parseRFC3339(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
