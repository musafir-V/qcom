package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/models"
)

// timedCompletedTrip: pickup completed 10:00Z, drop reached after lastMile
// minutes, rider back at the store returnScan minutes after the drop.
func timedCompletedTrip(orderID string, lastMile, returnScan int) *models.Trip {
	trip := completedTrip(orderID, "2026-01-15T11:00:00Z", 1)
	reached := "2026-01-15T10:00:00Z"
	if lastMile >= 0 {
		reached = addMinutes("2026-01-15T10:00:00Z", lastMile)
	}
	trip.Tasks = []models.Task{
		{Type: models.TaskTypePickup, Status: models.TaskStatusCompleted, CompletedAt: "2026-01-15T10:00:00Z"},
		{Type: models.TaskTypeDrop, Status: models.TaskStatusCompleted, ReachedAt: reached},
	}
	if returnScan >= 0 {
		trip.ReturnedAt = addMinutes(reached, returnScan)
	}
	return trip
}

func addMinutes(ts string, m int) string {
	tt, _ := time.Parse(time.RFC3339, ts)
	return tt.Add(time.Duration(m) * time.Minute).UTC().Format(time.RFC3339)
}

func floatPtrEq(p *float64, want float64) bool { return p != nil && *p == want }

func TestFetchDriverTrips_PerTripTimings(t *testing.T) {
	adminArrived := timedCompletedTrip("ORD-ADMIN", 9, 10)
	adminArrived.DropTask().ReachedByAdmin = true
	lister := &fakeTripLister{trips: []*models.Trip{
		timedCompletedTrip("ORD-OK", 12, 18),
		timedCompletedTrip("ORD-NORET", 7, -1),
		adminArrived,
	}}

	result, err := fetchDriverTrips(context.Background(), lister, "DE1", "start", "end", 0, 50)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	byOrder := map[string]driverTripItem{}
	for _, it := range result.Items {
		byOrder[it.OrderID] = it
	}

	ok := byOrder["ORD-OK"]
	if !floatPtrEq(ok.LastMileMin, 12) || !floatPtrEq(ok.ReturnMin, 15) || ok.MissingArrival {
		t.Fatalf("ORD-OK = %+v, want last mile 12, return 15 (18 − 3 allowance)", ok)
	}
	noRet := byOrder["ORD-NORET"]
	if !floatPtrEq(noRet.LastMileMin, 7) || noRet.ReturnMin != nil {
		t.Fatalf("ORD-NORET = %+v, want last mile 7 and no return", noRet)
	}
	admin := byOrder["ORD-ADMIN"]
	if admin.LastMileMin != nil || admin.ReturnMin != nil || !admin.MissingArrival {
		t.Fatalf("ORD-ADMIN = %+v, want no timings and missing_arrival", admin)
	}
}

func TestFetchDriverTrips_TimingStatsSpanWholeWindow(t *testing.T) {
	adminArrived := timedCompletedTrip("ORD-ADMIN", 90, 90)
	adminArrived.DropTask().ReachedByAdmin = true
	lister := &fakeTripLister{trips: []*models.Trip{
		timedCompletedTrip("A", 10, 13),
		timedCompletedTrip("B", 20, 23),
		timedCompletedTrip("C", 30, -1),
		adminArrived,
	}}

	// Page of one: stats must still cover every trip in the window.
	result, err := fetchDriverTrips(context.Background(), lister, "DE1", "start", "end", 0, 1)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !floatPtrEq(result.LastMileMedianMin, 20) || !floatPtrEq(result.LastMileP90Min, 28) {
		t.Fatalf("last mile median/p90 = %v/%v, want 20/28", deref(result.LastMileMedianMin), deref(result.LastMileP90Min))
	}
	if !floatPtrEq(result.ReturnMedianMin, 15) || !floatPtrEq(result.ReturnP90Min, 19) {
		t.Fatalf("return median/p90 = %v/%v, want 15/19", deref(result.ReturnMedianMin), deref(result.ReturnP90Min))
	}
	if result.MissingArrivalCount != 1 {
		t.Fatalf("MissingArrivalCount = %d, want 1", result.MissingArrivalCount)
	}
}

func TestFetchDriverTrips_NoTimingSamplesGivesNilStats(t *testing.T) {
	lister := &fakeTripLister{trips: []*models.Trip{completedTrip("OLD", "2026-01-15T11:00:00Z", 1)}}

	result, err := fetchDriverTrips(context.Background(), lister, "DE1", "start", "end", 0, 50)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if result.LastMileMedianMin != nil || result.ReturnMedianMin != nil || result.LastMileP90Min != nil || result.ReturnP90Min != nil {
		t.Fatalf("stats = %+v, want all nil without samples", result)
	}
}

func TestDriverTripsResponse_IncludesTimingSummary(t *testing.T) {
	median, p90 := 12.0, 20.0
	body := driverTripsResponse(&driverTripsResult{
		TripCount:           2,
		LastMileMedianMin:   &median,
		LastMileP90Min:      &p90,
		MissingArrivalCount: 1,
		Items:               []driverTripItem{},
	})

	if body["last_mile_median_min"] != &median || body["last_mile_p90_min"] != &p90 {
		t.Fatalf("last mile stats = %v / %v", body["last_mile_median_min"], body["last_mile_p90_min"])
	}
	if v, ok := body["return_median_min"]; !ok || v.(*float64) != nil {
		t.Fatalf("return_median_min = %v, want present and nil", v)
	}
	if _, ok := body["return_p90_min"]; !ok {
		t.Fatal("return_p90_min missing")
	}
	if body["missing_arrival_count"] != 1 || body["trip_count"] != 2 {
		t.Fatalf("counts = %v / %v", body["missing_arrival_count"], body["trip_count"])
	}
}

func deref(p *float64) interface{} {
	if p == nil {
		return nil
	}
	return *p
}
