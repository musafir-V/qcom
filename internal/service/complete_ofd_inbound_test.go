package service

import (
	"context"
	"errors"
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
	if !repo.adminOFDInboundCalled || !repo.trip.AdminOFDInbound {
		t.Fatal("no-rider OFD inbound must persist AdminOFDInbound")
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

func TestCompleteOFDInbound_AcceptedRider_SkipsAccept(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "T-OFD-4", OrderID: "ORD-OFD-4",
		DEID: "de-1", DEPhone: "+260971000001",
		Status: models.TripStatusAccepted,
		Tasks: []models.Task{
			{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
			{TaskID: "d", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
		},
	}}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "OUT_FOR_DELIVERY"}

	res, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-OFD-4", Status: "OUT_FOR_DELIVERY",
	})
	if err != nil || !res.Updated {
		t.Fatalf("got res=%+v err=%v", res, err)
	}
	if repo.trip.PickupTask().Status != models.TaskStatusCompleted {
		t.Fatal("expected pickup completed without re-Accept")
	}
}

func TestCompleteOFDInbound_MissingPickup(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "T-OFD-5", OrderID: "ORD-OFD-5",
		DEID: "de-1", DEPhone: "+260971000001",
		Status: models.TripStatusAccepted,
		Tasks:  []models.Task{{TaskID: "d", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated}},
	}}
	svc := newTripServiceForTest(repo, &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}, &stubNotifier{})

	_, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-OFD-5", Status: "OUT_FOR_DELIVERY",
	})
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("got %v, want ErrTaskNotFound", err)
	}
}

func TestCompleteOFDInbound_DENotFound(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "T-OFD-6", OrderID: "ORD-OFD-6",
		DEID: "de-1", DEPhone: "+260971000001",
		Status: models.TripStatusAssigned,
		Tasks: []models.Task{
			{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
		},
	}}
	svc := newTripServiceForTest(repo, &stubDERepo{}, &stubNotifier{})

	_, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-OFD-6", Status: "OUT_FOR_DELIVERY",
	})
	if !errors.Is(err, ErrDENotFound) {
		t.Fatalf("got %v, want ErrDENotFound", err)
	}
}

func TestCompleteOFDInbound_DELookupError(t *testing.T) {
	want := errors.New("de lookup")
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "T-OFD-8", OrderID: "ORD-OFD-8",
		DEID: "de-1", DEPhone: "+260971000001",
		Status: models.TripStatusAssigned,
		Tasks: []models.Task{
			{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
		},
	}}
	svc := newTripServiceForTest(repo, &stubDERepo{getErr: want}, &stubNotifier{})

	_, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-OFD-8", Status: "OUT_FOR_DELIVERY",
	})
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want de lookup error", err)
	}
}

func TestCompleteOFDInbound_AcceptError(t *testing.T) {
	want := errors.New("accept failed")
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID: "T-OFD-7", OrderID: "ORD-OFD-7",
			DEID: "de-1", DEPhone: "+260971000001",
			Status: models.TripStatusAssigned,
			Tasks: []models.Task{
				{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
			},
		},
		acceptErr: want,
	}
	svc := newTripServiceForTest(repo, &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}, &stubNotifier{})

	_, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-OFD-7", Status: "OUT_FOR_DELIVERY",
	})
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want accept error", err)
	}
}

func TestCompleteOFDInbound_ApplyError(t *testing.T) {
	want := errors.New("update tasks failed")
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID: "T-OFD-9", OrderID: "ORD-OFD-9",
			DEID: "de-1", DEPhone: "+260971000001",
			Status: models.TripStatusAccepted,
			Tasks: []models.Task{
				{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
			},
		},
		updateTasksFn: func(ctx context.Context, tripID string, tasks []models.Task) error {
			return want
		},
	}
	svc := newTripServiceForTest(repo, &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}, &stubNotifier{})

	_, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-OFD-9", Status: "OUT_FOR_DELIVERY",
	})
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want apply error", err)
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
