package service

import (
	"context"
	"testing"

	"github.com/qcom/qcom/internal/models"
)

func TestCompleteDeliveredInbound_WithRider_ClosesTripAndFrees(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID:  "T-DEL-1",
		OrderID: "ORD-DEL-1",
		StoreID: "221",
		DEID:    "de-1",
		DEPhone: "+260971000001",
		Status:  models.TripStatusAccepted,
		Tasks: []models.Task{
			{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
			{TaskID: "d", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
		},
	}}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	java := &stubJavaOrder{status: "DELIVERED"}
	svc.javaClient = java

	res, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-DEL-1", Status: "DELIVERED",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !res.Updated {
		t.Fatalf("expected updated=true, got %+v", res)
	}
	if !repo.completeAndFreeCalled {
		t.Fatal("rider path must CompleteTripAndFreeDE")
	}
	if repo.completeOnlyCalled {
		t.Fatal("rider path must not CompleteTripOnly")
	}
	if len(java.updates) != 0 {
		t.Fatalf("must not forceJavaDeliver / UpdateOrderStatus, got %v", java.updates)
	}
}

func TestCompleteDeliveredInbound_NoRider_CompleteTripOnly(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID:  "T-DEL-2",
		OrderID: "ORD-DEL-2",
		Status:  models.TripStatusCreated,
		Tasks: []models.Task{
			{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
			{TaskID: "d", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
		},
	}}
	svc := newTripServiceForTest(repo, &stubDERepo{}, &stubNotifier{})
	java := &stubJavaOrder{status: "DELIVERED"}
	svc.javaClient = java

	res, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-DEL-2", Status: "DELIVERED",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !res.Updated {
		t.Fatalf("expected updated=true, got %+v", res)
	}
	if !repo.completeOnlyCalled {
		t.Fatal("no-rider path must CompleteTripOnly")
	}
	if repo.completeAndFreeCalled || repo.completeTripCalled {
		t.Fatal("no-rider path must not free a rider")
	}
	if len(java.updates) != 0 {
		t.Fatalf("must not call Java, got %v", java.updates)
	}
	pickup, drop := repo.trip.PickupTask(), repo.trip.DropTask()
	if pickup == nil || pickup.Status != models.TaskStatusCompleted {
		t.Fatalf("expected pickup synthesized completed, got %+v", pickup)
	}
	if drop == nil || drop.Status != models.TaskStatusCompleted {
		t.Fatalf("expected drop synthesized completed, got %+v", drop)
	}
	if pickup.CompletedAt == "" || drop.CompletedAt == "" {
		t.Fatal("synthesized tasks must have CompletedAt")
	}
}
