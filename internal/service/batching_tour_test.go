package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/qcom/qcom/internal/models"
)

type scriptedDist struct {
	km map[string]float64
}

func legKey(oLat, oLng, dLat, dLng float64) string {
	return fmt.Sprintf("%.4f,%.4f->%.4f,%.4f", oLat, oLng, dLat, dLng)
}

func (s scriptedDist) DistanceKM(_ context.Context, oLat, oLng, dLat, dLng float64) (float64, error) {
	v, ok := s.km[legKey(oLat, oLng, dLat, dLng)]
	if !ok {
		return 0, fmt.Errorf("no leg %s", legKey(oLat, oLng, dLat, dLng))
	}
	return v, nil
}

func TestDisplayedTripKM_ShortBatchUsesFullPath(t *testing.T) {
	// Stored 0.7 is only drop1→drop2. The row must show store→drop1 + drop1→drop2.
	storeLat, storeLng := -15.3810, 28.3105
	aLat, aLng := -15.4026, 28.2913
	bLat, bLng := -15.4061, 28.2946
	trip := &models.Trip{
		DistanceKM: 0.7,
		Tasks: []models.Task{
			{Type: models.TaskTypePickup, Lat: storeLat, Lng: storeLng},
			{Type: models.TaskTypeDrop, OrderID: "ORD-A", Lat: aLat, Lng: aLng},
			{Type: models.TaskTypeDrop, OrderID: "ORD-B", Lat: bLat, Lng: bLng},
		},
	}
	dist := scriptedDist{km: map[string]float64{
		legKey(storeLat, storeLng, aLat, aLng): 4,
		legKey(aLat, aLng, bLat, bLng):         2,
	}}
	got := DisplayedTripKM(context.Background(), dist, trip)
	if got != 6 {
		t.Fatalf("displayed km = %v, want 6", got)
	}
}

func TestDisplayedTripKM_SecondLegOnlyStillShowsFullPath(t *testing.T) {
	// Saved 6 is only drop1→drop2. That driving hop is longer than the straight
	// line of the whole path, and the row must still show store→drop1 + drop1→drop2.
	storeLat, storeLng := -15.381012, 28.31048
	aLat, aLng := -15.414948, 28.3310082
	bLat, bLng := -15.4009331, 28.3209973
	trip := &models.Trip{
		DistanceKM: 6,
		Tasks: []models.Task{
			{Type: models.TaskTypePickup, Lat: storeLat, Lng: storeLng},
			{Type: models.TaskTypeDrop, OrderID: "ORD-A", Lat: aLat, Lng: aLng},
			{Type: models.TaskTypeDrop, OrderID: "ORD-B", Lat: bLat, Lng: bLng},
		},
	}
	dist := scriptedDist{km: map[string]float64{
		legKey(storeLat, storeLng, aLat, aLng): 9,
		legKey(aLat, aLng, bLat, bLng):         6,
	}}
	got := DisplayedTripKM(context.Background(), dist, trip)
	if got != 15 {
		t.Fatalf("displayed km = %v, want 15 (store→drop1 + drop1→drop2)", got)
	}
}

func TestNearestNeighbourTour_SumsStoreToDropThenDropToDrop(t *testing.T) {
	// store→A is 1.5, store→B is 4.0, A→B is 2.2.
	// The tour km is 1.5+2.2 = 3.7, not 2× store→A (3.0) and not store→A + store→B (5.5).
	drops := []models.Task{
		{TaskID: "B", Type: models.TaskTypeDrop, OrderID: "ORD-B", Lat: 2, Lng: 0},
		{TaskID: "A", Type: models.TaskTypeDrop, OrderID: "ORD-A", Lat: 1, Lng: 0},
	}
	dist := scriptedDist{km: map[string]float64{
		legKey(0, 0, 1, 0): 1.5,
		legKey(0, 0, 2, 0): 4.0,
		legKey(1, 0, 2, 0): 2.2,
		legKey(2, 0, 1, 0): 2.2,
	}}

	got := nearestNeighbourTour(context.Background(), dist, 0, 0, drops, "")
	if got.KM != 3.7 {
		t.Fatalf("km = %v, want 3.7 (store→A + A→B)", got.KM)
	}
	if len(got.Drops) != 2 || got.Drops[0].OrderID != "ORD-A" || got.Drops[1].OrderID != "ORD-B" {
		t.Fatalf("drop order = %s then %s, want ORD-A then ORD-B", got.Drops[0].OrderID, got.Drops[1].OrderID)
	}
}
