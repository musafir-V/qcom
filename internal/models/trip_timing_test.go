package models

import "testing"

func timedTrip(pickupCompleted, dropReached, returned string) *Trip {
	return &Trip{
		TripID:     "trip-1",
		ReturnedAt: returned,
		Tasks: []Task{
			{TaskID: "p", Type: TaskTypePickup, Status: TaskStatusCompleted, CompletedAt: pickupCompleted},
			{TaskID: "d", Type: TaskTypeDrop, Status: TaskStatusReached, ReachedAt: dropReached},
		},
	}
}

func TestTrip_LastMileMinutes_PickupCompletedToDropReached(t *testing.T) {
	trip := timedTrip("2026-11-06T10:00:00+02:00", "2026-11-06T10:12:30+02:00", "")
	got, ok := trip.LastMileMinutes()
	if !ok || got != 12.5 {
		t.Fatalf("LastMileMinutes() = %v, %v; want 12.5, true", got, ok)
	}
}

func TestTrip_LastMileMinutes_MissingReachedAt(t *testing.T) {
	trip := timedTrip("2026-11-06T10:00:00+02:00", "", "")
	if _, ok := trip.LastMileMinutes(); ok {
		t.Fatal("LastMileMinutes() ok = true without drop reached_at")
	}
}

func TestTrip_LastMileMinutes_AdminReachedIsUnreliable(t *testing.T) {
	trip := timedTrip("2026-11-06T10:00:00+02:00", "2026-11-06T10:12:00+02:00", "")
	trip.DropTask().ReachedByAdmin = true
	if _, ok := trip.LastMileMinutes(); ok {
		t.Fatal("LastMileMinutes() ok = true for an admin-synthesized arrival")
	}
}

func TestTrip_ReturnMinutes_SubtractsHandoverAllowance(t *testing.T) {
	// reached 10:12 local (08:12Z), returned 08:30Z → 18 min − 3 min allowance.
	trip := timedTrip("2026-11-06T10:00:00+02:00", "2026-11-06T10:12:00+02:00", "2026-11-06T08:30:00Z")
	got, ok := trip.ReturnMinutes()
	if !ok || got != 15 {
		t.Fatalf("ReturnMinutes() = %v, %v; want 15, true", got, ok)
	}
}

func TestTrip_ReturnMinutes_ClampsAtZero(t *testing.T) {
	trip := timedTrip("2026-11-06T10:00:00+02:00", "2026-11-06T10:12:00+02:00", "2026-11-06T08:13:00Z")
	got, ok := trip.ReturnMinutes()
	if !ok || got != 0 {
		t.Fatalf("ReturnMinutes() = %v, %v; want 0, true", got, ok)
	}
}

func TestTrip_ReturnMinutes_MissingReturnedAt(t *testing.T) {
	trip := timedTrip("2026-11-06T10:00:00+02:00", "2026-11-06T10:12:00+02:00", "")
	if _, ok := trip.ReturnMinutes(); ok {
		t.Fatal("ReturnMinutes() ok = true without returned_at")
	}
}

func TestTrip_ReturnMinutes_AdminReachedIsUnreliable(t *testing.T) {
	trip := timedTrip("2026-11-06T10:00:00+02:00", "2026-11-06T10:12:00+02:00", "2026-11-06T08:30:00Z")
	trip.DropTask().ReachedByAdmin = true
	if _, ok := trip.ReturnMinutes(); ok {
		t.Fatal("ReturnMinutes() ok = true for an admin-synthesized arrival")
	}
}

func TestTrip_MissingArrival(t *testing.T) {
	trip := timedTrip("2026-11-06T10:00:00+02:00", "2026-11-06T10:12:00+02:00", "")
	if trip.MissingArrival() {
		t.Fatal("MissingArrival() = true for a driver-marked arrival")
	}
	trip.DropTask().ReachedByAdmin = true
	if !trip.MissingArrival() {
		t.Fatal("MissingArrival() = false for an admin-synthesized arrival")
	}
}
