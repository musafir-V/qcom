package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/qcom/qcom/internal/models"
)

func TestDriverTripsWindow(t *testing.T) {
	t.Run("7-day range succeeds", func(t *testing.T) {
		start, end, err := catWindowRFC3339Max("2026-01-01", "2026-01-07", 7)
		if err != nil {
			t.Fatalf("catWindowRFC3339Max(2026-01-01, 2026-01-07, 7) unexpected err: %v", err)
		}
		if start != "2025-12-31T22:00:00Z" {
			t.Errorf("start = %q, want %q", start, "2025-12-31T22:00:00Z")
		}
		if end != "2026-01-07T21:59:59Z" {
			t.Errorf("end = %q, want %q", end, "2026-01-07T21:59:59Z")
		}
	})

	t.Run("8-day range is rejected", func(t *testing.T) {
		_, _, err := catWindowRFC3339Max("2026-01-01", "2026-01-08", 7)
		if !errors.Is(err, errCashCollectionsRangeTooWide) {
			t.Fatalf("catWindowRFC3339Max(2026-01-01, 2026-01-08, 7) err = %v, want wrapping %v", err, errCashCollectionsRangeTooWide)
		}
	})
}

func completedTrip(orderID, completedAt string, distanceKM float64) *models.Trip {
	return &models.Trip{
		OrderID:     orderID,
		Status:      models.TripStatusCompleted,
		CompletedAt: completedAt,
		DistanceKM:  distanceKM,
		Payment:     &models.Payment{CollectCash: true, AmountZMW: 10, Method: "COD"},
	}
}

func TestFetchDriverTrips_IncludesPrepaidAndNilPayment(t *testing.T) {
	lister := &fakeTripLister{
		trips: []*models.Trip{
			completedTrip("ORD-COD", "2026-01-15T10:00:00Z", 4.25),
			{
				OrderID:     "ORD-PREPAID",
				Status:      models.TripStatusCompleted,
				CompletedAt: "2026-01-15T09:00:00Z",
				DistanceKM:  3.0,
				Payment:     &models.Payment{CollectCash: false, AmountZMW: 50, Method: "AIRTEL_MONEY"},
			},
			{
				OrderID:     "ORD-NIL",
				Status:      models.TripStatusCompleted,
				CompletedAt: "2026-01-15T08:00:00Z",
				DistanceKM:  2.0,
				Payment:     nil,
			},
		},
	}

	result, err := fetchDriverTrips(context.Background(), lister, "DE1", "start", "end", 0, 50)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if result.TripCount != 3 {
		t.Fatalf("TripCount = %d, want 3 (COD + prepaid + nil payment must all appear)", result.TripCount)
	}
	got := map[string]bool{}
	for _, item := range result.Items {
		got[item.OrderID] = true
	}
	for _, id := range []string{"ORD-COD", "ORD-PREPAID", "ORD-NIL"} {
		if !got[id] {
			t.Errorf("missing order %q in items", id)
		}
	}
}

func TestFetchDriverTrips_SumsDistanceIncludingZero(t *testing.T) {
	lister := &fakeTripLister{
		trips: []*models.Trip{
			completedTrip("ORD-A", "2026-01-15T10:00:00Z", 4.25),
			completedTrip("ORD-B", "2026-01-15T09:00:00Z", 0),
			completedTrip("ORD-C", "2026-01-15T08:00:00Z", 8.25),
		},
	}

	result, err := fetchDriverTrips(context.Background(), lister, "DE1", "start", "end", 0, 50)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if result.TotalDistanceKM != 12.5 {
		t.Fatalf("TotalDistanceKM = %v, want 12.5", result.TotalDistanceKM)
	}
	if len(result.Items) != 3 {
		t.Fatalf("got %d items, want 3 (zero-distance row must still be listed)", len(result.Items))
	}
	foundZero := false
	for _, item := range result.Items {
		if item.OrderID == "ORD-B" {
			foundZero = true
			if item.DistanceKM != 0 {
				t.Errorf("ORD-B DistanceKM = %v, want 0", item.DistanceKM)
			}
		}
	}
	if !foundZero {
		t.Fatal("zero-distance ORD-B missing from items")
	}
}

func TestFetchDriverTrips_NewestFirst(t *testing.T) {
	lister := &fakeTripLister{
		trips: []*models.Trip{
			completedTrip("ORD-NEWEST", "2026-01-15T12:00:00Z", 1),
			completedTrip("ORD-MID", "2026-01-15T10:00:00Z", 1),
			completedTrip("ORD-OLDEST", "2026-01-15T08:00:00Z", 1),
		},
	}

	result, err := fetchDriverTrips(context.Background(), lister, "DE1", "start", "end", 0, 50)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("got %d items, want 3", len(result.Items))
	}
	want := []string{"ORD-NEWEST", "ORD-MID", "ORD-OLDEST"}
	for i, id := range want {
		if result.Items[i].OrderID != id {
			t.Errorf("items[%d].OrderID = %q, want %q (newest-first order not preserved)", i, result.Items[i].OrderID, id)
		}
	}
}

func TestFetchDriverTrips_TotalsSpanAllPages(t *testing.T) {
	var trips []*models.Trip
	for i := 0; i < 250; i++ {
		trips = append(trips, completedTrip("ORD"+itoa(i), "2026-01-15T10:00:00Z", 2))
	}
	lister := &fakeTripLister{trips: trips}

	result, err := fetchDriverTrips(context.Background(), lister, "DE1", "start", "end", 0, 20)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if result.TripCount != 250 {
		t.Errorf("TripCount = %d, want 250 (must recompute over full window, not just first page)", result.TripCount)
	}
	if result.TotalDistanceKM != 500 {
		t.Errorf("TotalDistanceKM = %v, want 500", result.TotalDistanceKM)
	}
	if len(result.Items) != 20 {
		t.Errorf("items returned = %d, want 20 (respecting caller's limit)", len(result.Items))
	}
	if result.NextCursor == "" {
		t.Errorf("expected a next_cursor since 250 > 20")
	}
	if lister.pagesFetched < 3 {
		t.Errorf("pagesFetched = %d, want at least 3 (250 trips / 100 page size)", lister.pagesFetched)
	}
}

func TestFetchDriverTrips_CursorPagination(t *testing.T) {
	lister := &fakeTripLister{
		trips: []*models.Trip{
			completedTrip("ORD-A", "2026-01-15T12:00:00Z", 1),
			completedTrip("ORD-B", "2026-01-15T11:00:00Z", 2),
			completedTrip("ORD-C", "2026-01-15T10:00:00Z", 3),
		},
	}

	page1, err := fetchDriverTrips(context.Background(), lister, "DE1", "start", "end", 0, 2)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(page1.Items) != 2 || page1.Items[0].OrderID != "ORD-A" || page1.Items[1].OrderID != "ORD-B" {
		t.Fatalf("page1 items = %+v, want [ORD-A, ORD-B]", page1.Items)
	}
	if page1.NextCursor == "" {
		t.Fatalf("expected next_cursor on page1")
	}
	if page1.TripCount != 3 || page1.TotalDistanceKM != 6 {
		t.Fatalf("page1 totals = (%d, %v), want (3, 6) — must reflect full window even on page 1", page1.TripCount, page1.TotalDistanceKM)
	}

	offset, err := decodeCashCollectionsCursor(page1.NextCursor)
	if err != nil {
		t.Fatalf("failed to decode next_cursor: %v", err)
	}
	page2, err := fetchDriverTrips(context.Background(), lister, "DE1", "start", "end", offset, 2)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(page2.Items) != 1 || page2.Items[0].OrderID != "ORD-C" {
		t.Fatalf("page2 items = %+v, want [ORD-C]", page2.Items)
	}
	if page2.NextCursor != "" {
		t.Errorf("page2 next_cursor = %q, want empty (no more pages)", page2.NextCursor)
	}
	if page2.TripCount != 3 || page2.TotalDistanceKM != 6 {
		t.Fatalf("page2 totals = (%d, %v), want (3, 6)", page2.TripCount, page2.TotalDistanceKM)
	}
}

func TestFetchDriverTrips_EmptyWindow(t *testing.T) {
	lister := &fakeTripLister{trips: nil}

	result, err := fetchDriverTrips(context.Background(), lister, "DE1", "start", "end", 0, 50)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if result.TripCount != 0 || result.TotalDistanceKM != 0 {
		t.Errorf("empty window result = %+v, want zero totals", result)
	}
	if result.Items == nil {
		t.Errorf("Items should be an empty slice, not nil, so JSON encodes as [] not null")
	}
	if result.NextCursor != "" {
		t.Errorf("next_cursor = %q, want empty", result.NextCursor)
	}
}

func TestFetchDriverTrips_ListerError(t *testing.T) {
	lister := &fakeTripLister{err: errors.New("dynamo unavailable")}

	_, err := fetchDriverTrips(context.Background(), lister, "DE1", "start", "end", 0, 50)
	if err == nil {
		t.Fatal("expected error to propagate from lister")
	}
}

func TestDriverTripsMaxRangeDays(t *testing.T) {
	if driverTripsMaxRangeDays != 7 {
		t.Fatalf("driverTripsMaxRangeDays = %d, want 7", driverTripsMaxRangeDays)
	}
}
