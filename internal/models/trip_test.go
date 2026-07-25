package models

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func sampleTrip() *Trip {
	return &Trip{
		TripID: "trip-123",
		Tasks: []Task{
			{TaskID: "task-pickup", Type: TaskTypePickup, Status: TaskStatusCreated},
			{TaskID: "task-drop", Type: TaskTypeDrop, Status: TaskStatusCreated, OTP: "4321"},
		},
	}
}

func TestTrip_GetPK(t *testing.T) {
	trip := &Trip{TripID: "trip-123"}
	if got := trip.GetPK(); got != "TRIP!trip-123" {
		t.Errorf("GetPK() = %q, want %q", got, "TRIP!trip-123")
	}
}

func TestTrip_GetSK(t *testing.T) {
	trip := &Trip{TripID: "trip-123"}
	if got := trip.GetSK(); got != "METADATA" {
		t.Errorf("GetSK() = %q, want %q", got, "METADATA")
	}
}

func TestTrip_PickupTask(t *testing.T) {
	trip := sampleTrip()
	pickup := trip.PickupTask()
	if pickup == nil {
		t.Fatal("PickupTask() returned nil")
	}
	if pickup.TaskID != "task-pickup" || pickup.Type != TaskTypePickup {
		t.Errorf("PickupTask() = %+v, want the pickup task", pickup)
	}
}

func TestTrip_PickupTask_None(t *testing.T) {
	trip := &Trip{TripID: "t", Tasks: []Task{{TaskID: "d", Type: TaskTypeDrop}}}
	if got := trip.PickupTask(); got != nil {
		t.Errorf("PickupTask() = %+v, want nil when no pickup task exists", got)
	}
}

func TestTrip_DropTask(t *testing.T) {
	trip := sampleTrip()
	drop := trip.DropTask()
	if drop == nil {
		t.Fatal("DropTask() returned nil")
	}
	if drop.TaskID != "task-drop" || drop.Type != TaskTypeDrop {
		t.Errorf("DropTask() = %+v, want the drop task", drop)
	}
	if drop.OTP != "4321" {
		t.Errorf("DropTask().OTP = %q, want %q", drop.OTP, "4321")
	}
}

func TestTrip_DropTask_None(t *testing.T) {
	trip := &Trip{TripID: "t", Tasks: []Task{{TaskID: "p", Type: TaskTypePickup}}}
	if got := trip.DropTask(); got != nil {
		t.Errorf("DropTask() = %+v, want nil when no drop task exists", got)
	}
}

func TestTrip_TaskByID(t *testing.T) {
	trip := sampleTrip()
	task := trip.TaskByID("task-drop")
	if task == nil {
		t.Fatal("TaskByID(task-drop) returned nil")
	}
	if task.TaskID != "task-drop" {
		t.Errorf("TaskByID() = %+v, want task-drop", task)
	}
}

func TestTrip_TaskByID_NotFound(t *testing.T) {
	trip := sampleTrip()
	if got := trip.TaskByID("missing"); got != nil {
		t.Errorf("TaskByID(missing) = %+v, want nil", got)
	}
}

// Mutating the returned task pointer must mutate the trip's embedded task.
func TestTrip_TaskByID_ReturnsPointerIntoSlice(t *testing.T) {
	trip := sampleTrip()
	trip.TaskByID("task-drop").Status = TaskStatusCompleted
	if trip.Tasks[1].Status != TaskStatusCompleted {
		t.Errorf("expected embedded drop task status to be updated, got %q", trip.Tasks[1].Status)
	}
}

func TestTrip_ActualDeliveryMinutes(t *testing.T) {
	trip := &Trip{
		Tasks: []Task{
			{TaskID: "task-pickup", Type: TaskTypePickup, CompletedAt: "2026-06-22T10:00:00+02:00"},
			{TaskID: "task-drop", Type: TaskTypeDrop, CompletedAt: "2026-06-22T10:45:00+02:00"},
		},
	}

	got, ok := trip.ActualDeliveryMinutes()
	if !ok {
		t.Fatal("ActualDeliveryMinutes() ok = false, want true")
	}
	if got != 45 {
		t.Fatalf("ActualDeliveryMinutes() = %v, want 45", got)
	}
}

func TestTrip_ActualDeliveryMinutes_MissingDropTimestamp(t *testing.T) {
	trip := &Trip{
		Tasks: []Task{
			{TaskID: "task-pickup", Type: TaskTypePickup, CompletedAt: "2026-06-22T10:00:00+02:00"},
			{TaskID: "task-drop", Type: TaskTypeDrop},
		},
	}

	got, ok := trip.ActualDeliveryMinutes()
	if ok {
		t.Fatal("ActualDeliveryMinutes() ok = true, want false")
	}
	if got != 0 {
		t.Fatalf("ActualDeliveryMinutes() = %v, want 0", got)
	}
}

func TestIsValidTripTransition(t *testing.T) {
	valid := []struct{ from, to TripStatus }{
		{TripStatusCreated, TripStatusAssigned},
		{TripStatusCreated, TripStatusAccepted},        // admin hard-assign
		{TripStatusAssigned, TripStatusAccepted},       // driver accept
		{TripStatusAssigned, TripStatusCreated},        // reject / auto-reject
		{TripStatusAccepted, TripStatusOutForDelivery}, // pickup done
		{TripStatusOutForDelivery, TripStatusCompleted},// drop done
		{TripStatusCreated, TripStatusCancelled},
		{TripStatusAssigned, TripStatusCancelled},
		{TripStatusAccepted, TripStatusCancelled},
		{TripStatusOutForDelivery, TripStatusCancelled},
	}
	for _, c := range valid {
		if !IsValidTripTransition(c.from, c.to) {
			t.Errorf("expected %s -> %s to be valid", c.from, c.to)
		}
	}

	invalid := []struct{ from, to TripStatus }{
		{TripStatusAccepted, TripStatusCreated},          // no reject after accept
		{TripStatusAssigned, TripStatusOutForDelivery},   // must accept first
		{TripStatusCreated, TripStatusOutForDelivery},
		{TripStatusCompleted, TripStatusCreated},
		{TripStatusCancelled, TripStatusAssigned},
		{TripStatusOutForDelivery, TripStatusAccepted},
	}
	for _, c := range invalid {
		if IsValidTripTransition(c.from, c.to) {
			t.Errorf("expected %s -> %s to be INVALID", c.from, c.to)
		}
	}
}

func TestHasRejected(t *testing.T) {
	trip := &Trip{RejectedDEIDs: []string{"de-1", "de-2"}}
	if !trip.HasRejected("de-1") {
		t.Error("expected de-1 to be rejected")
	}
	if trip.HasRejected("de-3") {
		t.Error("expected de-3 to NOT be rejected")
	}
}

func TestTripSyncIndexKeys(t *testing.T) {
	trip := &Trip{OrderID: "ORD123"}
	trip.SyncIndexKeys()
	if trip.TripOrderID != "ORD123" {
		t.Errorf("TripOrderID = %q, want ORD123", trip.TripOrderID)
	}
}

func TestTripMarshalHasTripOrderID(t *testing.T) {
	trip := &Trip{
		TripID:  "trip-1",
		OrderID: "ORD123",
		Status:  TripStatusCreated,
	}
	trip.SyncIndexKeys()

	m, err := attributevalue.MarshalMap(trip)
	if err != nil {
		t.Fatalf("MarshalMap failed: %v", err)
	}
	if _, ok := m["trip_order_id"]; !ok {
		t.Errorf("marshaled map must have trip_order_id for OrderIndex GSI, got keys: %v", m)
	}
	if v, ok := m["trip_order_id"].(*types.AttributeValueMemberS); !ok || v.Value != "ORD123" {
		t.Errorf("trip_order_id = %v, want ORD123", m["trip_order_id"])
	}
	if _, ok := m["order_id"]; !ok {
		t.Errorf("marshaled map must still have order_id for API/display, got keys: %v", m)
	}
}

func TestIsValidReassignReasonCode(t *testing.T) {
	valid := []string{
		"bike_breakdown", "rider_unreachable", "rider_sick_or_accident",
		"rider_too_far", "cash_limit_reached", "customer_request", "other",
	}
	for _, code := range valid {
		if !IsValidReassignReasonCode(code) {
			t.Fatalf("expected %q to be a valid reason code", code)
		}
	}
	for _, code := range []string{"", "BIKE_BREAKDOWN", "bogus", " other"} {
		if IsValidReassignReasonCode(code) {
			t.Fatalf("expected %q to be rejected", code)
		}
	}
}

// Reassignments carries the ops admin's username and a reason code about the
// rider. GetCurrentTrip returns *models.Trip straight to the driver app, so this
// field must never appear in the JSON a rider receives.
func TestTripReassignments_NotSerialisedToJSON(t *testing.T) {
	trip := Trip{
		TripID: "T1",
		Reassignments: []TripReassignment{{
			FromDEID: "DE-1", ToDEID: "DE-2",
			ReasonCode: "bike_breakdown", AdminUsername: "ops_jane",
		}},
	}
	raw, err := json.Marshal(trip)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, needle := range []string{"reassignments", "ops_jane", "bike_breakdown", "DE-1"} {
		if strings.Contains(string(raw), needle) {
			t.Fatalf("driver-facing trip JSON leaked %q: %s", needle, raw)
		}
	}
}
