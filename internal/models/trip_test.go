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
		{TripStatusCreated, TripStatusInactive},
		{TripStatusAssigned, TripStatusInactive},
		{TripStatusAccepted, TripStatusInactive},
		{TripStatusOutForDelivery, TripStatusInactive},
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
		{TripStatusInactive, TripStatusAccepted},
		{TripStatusInactive, TripStatusCreated},
		{TripStatusCompleted, TripStatusInactive},
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
	if len(trip.OrderIDs) != 1 || trip.OrderIDs[0] != "ORD123" {
		t.Errorf("OrderIDs = %v, want [ORD123]", trip.OrderIDs)
	}
}

func TestTripSyncIndexKeys_OrderIDsWins(t *testing.T) {
	trip := &Trip{OrderID: "OLD", OrderIDs: []string{"A", "B"}}
	trip.SyncIndexKeys()
	if trip.OrderID != "A" {
		t.Errorf("OrderID = %q, want A", trip.OrderID)
	}
	if trip.TripOrderID != "A" {
		t.Errorf("TripOrderID = %q, want A", trip.TripOrderID)
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

func TestTripJSON_DropDeadlineOmittedWhenNil(t *testing.T) {
	trip := &Trip{TripID: "t1"}
	raw, err := json.Marshal(trip)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "drop_deadline") {
		t.Fatalf("drop_deadline should be omitted when nil: %s", raw)
	}
}

func TestTripJSON_DropDeadlineEpochSecondsNumber(t *testing.T) {
	deadline := int64(1710000000)
	trip := &Trip{TripID: "t1", DropDeadline: &deadline}
	raw, err := json.Marshal(trip)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"drop_deadline":"`) {
		t.Fatalf("drop_deadline must be a JSON number, not a string: %s", raw)
	}
	if !strings.Contains(string(raw), `"drop_deadline":1710000000`) {
		t.Fatalf("expected epoch seconds number 1710000000, got %s", raw)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	v, ok := decoded["drop_deadline"].(float64)
	if !ok || int64(v) != 1710000000 {
		t.Fatalf("drop_deadline = %v (%T), want 1710000000 number", decoded["drop_deadline"], decoded["drop_deadline"])
	}
}

func TestTripDynamo_DropDeadlineNumber(t *testing.T) {
	deadline := int64(1710000000)
	trip := &Trip{TripID: "t1", DropDeadline: &deadline}
	m, err := attributevalue.MarshalMap(trip)
	if err != nil {
		t.Fatalf("MarshalMap: %v", err)
	}
	av, ok := m["drop_deadline"]
	if !ok {
		t.Fatal("missing drop_deadline")
	}
	n, ok := av.(*types.AttributeValueMemberN)
	if !ok {
		t.Fatalf("drop_deadline type = %T, want AttributeValueMemberN", av)
	}
	if n.Value != "1710000000" {
		t.Fatalf("drop_deadline = %q, want 1710000000", n.Value)
	}
}

func TestTrip_BatchedTaskHelpers(t *testing.T) {
	dlA := int64(100)
	dlB := int64(50)
	trip := &Trip{
		OrderID:  "A",
		OrderIDs: []string{"A", "B"},
		Tasks: []Task{
			{TaskID: "pA", Type: TaskTypePickup, OrderID: "A", Status: TaskStatusCompleted, Items: []TripItem{{Name: "Milk"}}},
			{TaskID: "pB", Type: TaskTypePickup, OrderID: "B", Status: TaskStatusCreated, Items: []TripItem{{Name: "Bread"}}},
			{TaskID: "dA", Type: TaskTypeDrop, OrderID: "A", Status: TaskStatusCreated, DropDeadline: &dlA, Payment: &Payment{CollectCash: true, AmountZMW: 10}},
			{TaskID: "dB", Type: TaskTypeDrop, OrderID: "B", Status: TaskStatusCreated, DropDeadline: &dlB, Payment: &Payment{CollectCash: false, AmountZMW: 5}},
		},
	}
	if p := trip.PickupForOrder("B"); p == nil || p.TaskID != "pB" {
		t.Fatalf("PickupForOrder B = %+v", p)
	}
	if d := trip.DropForOrder("A"); d == nil || d.TaskID != "dA" {
		t.Fatalf("DropForOrder A = %+v", d)
	}
	if got := trip.FirstIncompleteTask(); got == nil || got.TaskID != "pB" {
		t.Fatalf("FirstIncompleteTask = %+v, want pB", got)
	}
	if n := len(trip.OpenPickups()); n != 1 {
		t.Fatalf("OpenPickups = %d, want 1", n)
	}
	if n := len(trip.OpenDrops()); n != 2 {
		t.Fatalf("OpenDrops = %d, want 2", n)
	}
	min := trip.MinOpenDropDeadline()
	if min == nil || *min != 50 {
		t.Fatalf("MinOpenDropDeadline = %v, want 50", min)
	}
	roll := trip.PaymentRollup()
	if roll == nil || !roll.CollectCash || roll.AmountZMW != 10 {
		t.Fatalf("PaymentRollup = %+v, want COD 10", roll)
	}
	items := trip.ItemsRollup()
	if len(items) != 2 || items[0].Name != "Milk" || items[1].Name != "Bread" {
		t.Fatalf("ItemsRollup = %+v", items)
	}
}

func TestTrip_DropsOnTime(t *testing.T) {
	dl := int64(1_700_000_100)
	ok := &Trip{Tasks: []Task{
		{Type: TaskTypeDrop, CompletedAt: "2023-11-14T22:15:00Z", DropDeadline: &dl},
	}}
	if !ok.DropsOnTime() {
		t.Fatal("expected on-time when completed_at unix <= deadline")
	}
	late := &Trip{Tasks: []Task{
		{Type: TaskTypeDrop, CompletedAt: "2023-11-14T22:16:41Z", DropDeadline: &dl},
	}}
	if late.DropsOnTime() {
		t.Fatal("expected late when completed_at unix > deadline")
	}
	missing := &Trip{Tasks: []Task{
		{Type: TaskTypeDrop, CompletedAt: "2023-11-14T22:16:41Z"},
	}}
	if !missing.DropsOnTime() {
		t.Fatal("missing deadline must count as on-time")
	}
}

func TestIsLiveAndStaleTripStatus(t *testing.T) {
	if !IsLiveTripStatus(TripStatusAccepted) || IsLiveTripStatus(TripStatusInactive) {
		t.Fatal("live/inactive classification")
	}
	if !IsStaleCurrentTrip(TripStatusInactive) || IsStaleCurrentTrip(TripStatusAccepted) {
		t.Fatal("stale current-trip classification")
	}
}
