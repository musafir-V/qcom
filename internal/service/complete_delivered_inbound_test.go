package service

import (
	"context"
	"errors"
	"testing"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
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

func TestCompleteDeliveredInbound_NoRider_KeepsExistingCompletedAt(t *testing.T) {
	const existing = "2026-01-01T00:00:00Z"
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "T-DEL-3", OrderID: "ORD-DEL-3",
		Status: models.TripStatusCreated,
		Tasks: []models.Task{
			{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCompleted, CompletedAt: existing},
			{TaskID: "d", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
		},
	}}
	svc := newTripServiceForTest(repo, &stubDERepo{}, &stubNotifier{})

	res, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-DEL-3", Status: "DELIVERED",
	})
	if err != nil || !res.Updated {
		t.Fatalf("got res=%+v err=%v", res, err)
	}
	if got := repo.trip.PickupTask().CompletedAt; got != existing {
		t.Fatalf("CompletedAt = %q, want existing %q", got, existing)
	}
}

func TestCompleteDeliveredInbound_DENotFound(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "T-DEL-4", OrderID: "ORD-DEL-4",
		DEID: "de-1", DEPhone: "+260971000001",
		Status: models.TripStatusAccepted,
		Tasks: []models.Task{
			{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
			{TaskID: "d", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
		},
	}}
	svc := newTripServiceForTest(repo, &stubDERepo{}, &stubNotifier{})

	_, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-DEL-4", Status: "DELIVERED",
	})
	if !errors.Is(err, ErrDENotFound) {
		t.Fatalf("got %v, want ErrDENotFound", err)
	}
}

func TestCompleteDeliveredInbound_DELookupError(t *testing.T) {
	want := errors.New("de lookup")
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "T-DEL-5", OrderID: "ORD-DEL-5",
		DEID: "de-1", DEPhone: "+260971000001",
		Status: models.TripStatusAccepted,
		Tasks: []models.Task{
			{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
			{TaskID: "d", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
		},
	}}
	svc := newTripServiceForTest(repo, &stubDERepo{getErr: want}, &stubNotifier{})

	_, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-DEL-5", Status: "DELIVERED",
	})
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want de lookup error", err)
	}
}

func TestCompleteDeliveredInbound_MissingDrop(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "T-DEL-7", OrderID: "ORD-DEL-7", StoreID: "221",
		DEID: "de-1", DEPhone: "+260971000001",
		Status: models.TripStatusAccepted,
		Tasks: []models.Task{
			{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
		},
	}}
	svc := newTripServiceForTest(repo, &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}, &stubNotifier{})

	_, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-DEL-7", Status: "DELIVERED",
	})
	if err == nil {
		t.Fatal("expected error when drop task is missing")
	}
}

func TestCompleteDeliveredInbound_CompleteTripOnlyTerminalIsNoOp(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID: "T-DEL-TERM", OrderID: "ORD-DEL-TERM",
			Status: models.TripStatusCreated,
			Tasks: []models.Task{
				{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
				{TaskID: "d", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
			},
		},
		completeOnlyErr: repository.ErrTripTerminal,
	}
	svc := newTripServiceForTest(repo, &stubDERepo{}, &stubNotifier{})

	res, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-DEL-TERM", Status: "DELIVERED",
	})
	if err != nil {
		t.Fatalf("trip_terminal must not be a 500, got %v", err)
	}
	if res.Updated || res.Reason != "trip_terminal" {
		t.Fatalf("got %+v, want trip_terminal", res)
	}
}

func TestCompleteDeliveredInbound_CompleteTripOnlyError(t *testing.T) {
	want := errors.New("complete only failed")
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID: "T-DEL-6", OrderID: "ORD-DEL-6",
			Status: models.TripStatusCreated,
			Tasks: []models.Task{
				{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
				{TaskID: "d", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
			},
		},
		completeOnlyErr: want,
	}
	svc := newTripServiceForTest(repo, &stubDERepo{}, &stubNotifier{})

	_, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-DEL-6", Status: "DELIVERED",
	})
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want CompleteTripOnly error", err)
	}
}
