package service

import (
	"context"
	"errors"
	"testing"

	"github.com/qcom/qcom/internal/models"
)

func TestAutoCompletePickupIfJavaOFD_CompletesPickup(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID:  "T-AC-1",
		OrderID: "ORD-AC-1",
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
	svc.javaClient = &stubJavaOrder{status: "OUT_FOR_DELIVERY"}

	if err := svc.AutoCompletePickupIfJavaOFD(context.Background(), "ORD-AC-1"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	pickup := repo.trip.PickupTask()
	if pickup == nil || pickup.Status != models.TaskStatusCompleted {
		t.Fatalf("Java OFD must complete pickup, got %+v", pickup)
	}
}

func TestAutoCompletePickup_PriorOFDInboundNoRider_CompletesOnAssign(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID:  "T-AC-PRIOR",
		OrderID: "ORD-AC-PRIOR",
		Status:  models.TripStatusCreated,
		Tasks: []models.Task{
			{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
			{TaskID: "d", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
		},
	}}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}

	res, err := svc.CompleteByOrder(context.Background(), CompleteByOrderInput{
		OrderID: "ORD-AC-PRIOR", Status: "OUT_FOR_DELIVERY",
	})
	if err != nil {
		t.Fatalf("OFD inbound: %v", err)
	}
	if res.Updated || res.Reason != "no_rider" {
		t.Fatalf("expected no_rider, got %+v", res)
	}
	if !repo.trip.AdminOFDInbound {
		t.Fatal("no-rider OFD inbound must persist AdminOFDInbound")
	}
	if repo.trip.PickupTask().Status != models.TaskStatusCreated {
		t.Fatal("must not complete pickup before a rider is assigned")
	}

	repo.trip.DEID = "de-1"
	repo.trip.DEPhone = "+260971000001"
	repo.trip.Status = models.TripStatusAccepted

	if err := svc.AutoCompletePickupIfJavaOFD(context.Background(), "ORD-AC-PRIOR"); err != nil {
		t.Fatalf("auto-complete after assign: %v", err)
	}
	pickup := repo.trip.PickupTask()
	if pickup == nil || pickup.Status != models.TaskStatusCompleted {
		t.Fatalf("prior admin OFD inbound must complete pickup on assign even when Java is RFD, got %+v", pickup)
	}
}

func TestAutoCompletePickupIfJavaOFD_RFD_NoOp(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID:  "T-AC-2",
		OrderID: "ORD-AC-2",
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
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}

	if repo.trip.AdminOFDInbound {
		t.Fatal("fixture must leave AdminOFDInbound false")
	}
	if err := svc.AutoCompletePickupIfJavaOFD(context.Background(), "ORD-AC-2"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	pickup := repo.trip.PickupTask()
	if pickup == nil || pickup.Status != models.TaskStatusCreated {
		t.Fatalf("RFD must not complete pickup, got %+v", pickup)
	}
}

func TestAutoCompletePickupIfJavaOFD_NilJavaClient(t *testing.T) {
	svc := newTripServiceForTest(&stubTripRepo{}, &stubDERepo{}, &stubNotifier{})
	if err := svc.AutoCompletePickupIfJavaOFD(context.Background(), "ORD-NIL"); err != nil {
		t.Fatalf("nil java client must no-op, got %v", err)
	}
}

func TestAutoCompletePickupIfJavaOFD_JavaDelivered_CompletesPickup(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "T-AC-4", OrderID: "ORD-AC-4",
		DEID: "de-1", DEPhone: "+260971000001",
		Status: models.TripStatusAssigned,
		Tasks: []models.Task{
			{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
			{TaskID: "d", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
		},
	}}
	svc := newTripServiceForTest(repo, &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "DELIVERED"}

	if err := svc.AutoCompletePickupIfJavaOFD(context.Background(), "ORD-AC-4"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if repo.trip.PickupTask().Status != models.TaskStatusCompleted {
		t.Fatal("Java DELIVERED at assign must complete pickup")
	}
}

func TestAutoCompletePickupIfJavaOFD_JavaDown_DoesNotFailAssign(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID:  "T-AC-3",
		OrderID: "ORD-AC-3",
		DEID:    "de-1",
		Status:  models.TripStatusAssigned,
		Tasks: []models.Task{
			{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
		},
	}}
	svc := newTripServiceForTest(repo, &stubDERepo{}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{getErr: errors.New("java down")}

	if err := svc.AutoCompletePickupIfJavaOFD(context.Background(), "ORD-AC-3"); err != nil {
		t.Fatalf("Java down must not fail assign, got %v", err)
	}
	if repo.updateTasksCalled {
		t.Fatal("must not mutate trip when GetOrderStatus fails")
	}
}
