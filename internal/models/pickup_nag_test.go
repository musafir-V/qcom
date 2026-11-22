package models

import (
	"testing"
	"time"
)

func TestPickupNagAt_UsesTheLaterOfAcceptAndReady(t *testing.T) {
	accept := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	readyFirst := accept.Add(-4 * time.Minute)
	readyLater := accept.Add(4 * time.Minute)

	if got := PickupNagAt(accept, readyFirst); !got.Equal(accept.Add(time.Minute)) {
		t.Fatalf("ready before accept: nag = %s, want accept+1m", got)
	}
	if got := PickupNagAt(accept, readyLater); !got.Equal(readyLater.Add(time.Minute)) {
		t.Fatalf("ready after accept: nag = %s, want ready+1m", got)
	}
}

func TestPickupNagAt_MissingEitherTimestamp(t *testing.T) {
	at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	if !PickupNagAt(time.Time{}, at).IsZero() {
		t.Fatal("missing accept must not produce a nag time")
	}
	if !PickupNagAt(at, time.Time{}).IsZero() {
		t.Fatal("missing ready must not produce a nag time")
	}
}

func TestPickupNagBody_UsesRackOrFallback(t *testing.T) {
	if got := PickupNagBody("Blue Rack 2"); got != "Blue Rack 2" {
		t.Fatalf("body = %q", got)
	}
	if got := PickupNagBody("  "); got != PickupNagFallbackBody {
		t.Fatalf("blank rack body = %q, want %q", got, PickupNagFallbackBody)
	}
}

func TestPickupNagNotification_OnlyTheCurrentOpenPickup(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 5, 0, 0, time.UTC)
	accept := now.Add(-10 * time.Minute)
	readyA := now.Add(-2 * time.Minute)
	readyB := now.Add(-3 * time.Minute)

	trip := &Trip{
		TripID:     "trip-1",
		DEID:       "de-1",
		Status:     TripStatusAccepted,
		AcceptedAt: accept.Format(time.RFC3339),
		Tasks: []Task{
			{
				TaskID: "p-a", Type: TaskTypePickup, Status: TaskStatusCreated, OrderID: "ORD-A",
				DeliveryZone: "Blue Rack 2", ReadyAt: readyA.Format(time.RFC3339),
			},
			{
				TaskID: "p-b", Type: TaskTypePickup, Status: TaskStatusCreated, OrderID: "ORD-B",
				DeliveryZone: "Red Rack 1", ReadyAt: readyB.Format(time.RFC3339),
			},
		},
	}

	req, ok := trip.PickupNagNotification(now)
	if !ok {
		t.Fatal("expected a nag for the current pickup")
	}
	if req.EventType != "PICKUP_NAG" || req.Priority != PriorityCritical {
		t.Fatalf("request = %+v", req)
	}
	if req.Title != PickupNagTitle || req.Body != "Blue Rack 2" {
		t.Fatalf("copy = %q / %q", req.Title, req.Body)
	}
	if req.RecipientID != "de-1" || req.Data["trip_id"] != "trip-1" || req.Data["order_id"] != "ORD-A" {
		t.Fatalf("routing = %+v", req)
	}

	trip.Tasks[0].Status = TaskStatusCompleted
	req, ok = trip.PickupNagNotification(now)
	if !ok || req.Body != "Red Rack 1" || req.Data["order_id"] != "ORD-B" {
		t.Fatalf("after A is picked up, nag should move to B, got ok=%v %+v", ok, req)
	}
}

func TestPickupNagNotification_SilentUntilTheMinuteAndBetweenPushes(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 0, 30, 0, time.UTC)
	trip := &Trip{
		TripID:     "trip-1",
		DEID:       "de-1",
		Status:     TripStatusAccepted,
		AcceptedAt: now.Add(-2 * time.Minute).Format(time.RFC3339),
		Tasks: []Task{{
			TaskID: "p-a", Type: TaskTypePickup, Status: TaskStatusCreated, OrderID: "ORD-A",
			ReadyAt: now.Format(time.RFC3339),
		}},
	}
	if _, ok := trip.PickupNagNotification(now); ok {
		t.Fatal("must stay silent during the minute after ready")
	}

	trip.Tasks[0].ReadyAt = now.Add(-2 * time.Minute).Format(time.RFC3339)
	if _, ok := trip.PickupNagNotification(now); !ok {
		t.Fatal("expected a nag once the minute has passed")
	}
	trip.PickupNagPushAt = now.Add(-10 * time.Second).Format(time.RFC3339)
	if _, ok := trip.PickupNagNotification(now); ok {
		t.Fatal("must not push again inside 30 seconds")
	}
	trip.PickupNagPushAt = now.Add(-30 * time.Second).Format(time.RFC3339)
	if _, ok := trip.PickupNagNotification(now); !ok {
		t.Fatal("expected another push once 30 seconds have elapsed")
	}
}

func TestPickupNagNotification_StopsWhenTheTripIsNoLongerAccepted(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 5, 0, 0, time.UTC)
	trip := &Trip{
		TripID:     "trip-1",
		DEID:       "de-1",
		Status:     TripStatusCancelled,
		AcceptedAt: now.Add(-10 * time.Minute).Format(time.RFC3339),
		Tasks: []Task{{
			TaskID: "p-a", Type: TaskTypePickup, Status: TaskStatusCreated,
			ReadyAt: now.Add(-5 * time.Minute).Format(time.RFC3339),
		}},
	}
	if _, ok := trip.PickupNagNotification(now); ok {
		t.Fatal("a cancelled trip must not nag")
	}
}

func TestMarkOrderReady_StampsOnceAndKeepsTheEarlierTime(t *testing.T) {
	first := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	later := first.Add(5 * time.Minute)
	trip := &Trip{
		Status:     TripStatusAccepted,
		AcceptedAt: first.Add(-2 * time.Minute).Format(time.RFC3339),
		OrderID:    "ORD-A",
		Tasks:      []Task{{TaskID: "p-a", Type: TaskTypePickup, Status: TaskStatusCreated, OrderID: "ORD-A"}},
	}
	if !trip.MarkOrderReady("ORD-A", first) {
		t.Fatal("expected the first ready observation to stamp")
	}
	if trip.MarkOrderReady("ORD-A", later) {
		t.Fatal("a second observation must not overwrite ready_at")
	}
	if trip.Tasks[0].ReadyAt != first.Format(time.RFC3339) {
		t.Fatalf("ready_at = %s", trip.Tasks[0].ReadyAt)
	}
	want := first.Add(time.Minute).Format(time.RFC3339)
	if trip.Tasks[0].PickupNagAt != want {
		t.Fatalf("pickup_nag_at = %s, want %s", trip.Tasks[0].PickupNagAt, want)
	}
}
