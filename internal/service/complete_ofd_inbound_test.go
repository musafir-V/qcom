package service

import (
	"context"
	"testing"

	"github.com/qcom/qcom/internal/models"
)

func TestCompleteOFDInbound_NoRider_LeavesPickup(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID:  "T-OFD-1",
		OrderID: "ORD-OFD-1",
		Status:  models.TripStatusCreated,
		Tasks: []models.Task{
			{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
			{TaskID: "d", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
		},
	}}
	svc := newTripServiceForTest(repo, &stubDERepo{}, &stubNotifier{})
	java := &stubJavaOrder{status: "OUT_FOR_DELIVERY"}
	svc.javaClient = java

	res, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-OFD-1", Status: "OUT_FOR_DELIVERY",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Updated || res.Reason != "no_rider" {
		t.Fatalf("expected no_rider, got %+v", res)
	}
	if repo.updateTasksCalled {
		t.Fatal("must not complete pickup when no rider is on the trip")
	}
	pickup := repo.trip.PickupTask()
	if pickup == nil || pickup.Status != models.TaskStatusCreated {
		t.Fatalf("pickup must stay created, got %+v", pickup)
	}
	if len(java.updates) != 0 {
		t.Fatalf("must not call Java UpdateOrderStatus, got %v", java.updates)
	}
}

func TestCompleteOFDInbound_AssignedRider_CompletesPickup(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID:  "T-OFD-2",
		OrderID: "ORD-OFD-2",
		DEID:    "de-1",
		DEPhone: "+260971000001",
		Status:  models.TripStatusAssigned,
		Tasks: []models.Task{
			{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
			{TaskID: "d", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
		},
	}}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	java := &stubJavaOrder{status: "OUT_FOR_DELIVERY"}
	svc.javaClient = java

	res, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-OFD-2", Status: "OUT_FOR_DELIVERY",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !res.Updated {
		t.Fatalf("expected updated=true, got %+v", res)
	}
	pickup := repo.trip.PickupTask()
	if pickup == nil || pickup.Status != models.TaskStatusCompleted {
		t.Fatalf("expected pickup completed, got %+v", pickup)
	}
	if repo.trip.Status != models.TripStatusAccepted && !repo.updateTasksCalled {
		t.Fatal("expected Accept then pickup applyTaskCompletion")
	}
	if len(java.updates) != 0 {
		t.Fatalf("skipJava must suppress Java writes, got %v", java.updates)
	}
}

func TestCompleteOFDInbound_PickupAlreadyDone(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID:  "T-OFD-3",
		OrderID: "ORD-OFD-3",
		DEID:    "de-1",
		DEPhone: "+260971000001",
		Status:  models.TripStatusOutForDelivery,
		Tasks: []models.Task{
			{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCompleted, CompletedAt: "2026-09-01T00:00:00Z"},
			{TaskID: "d", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
		},
	}}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "OUT_FOR_DELIVERY"}

	res, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-OFD-3", Status: "OUT_FOR_DELIVERY",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Updated || res.Reason != "already_done" {
		t.Fatalf("expected already_done, got %+v", res)
	}
}
