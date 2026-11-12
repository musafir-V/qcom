package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

func batchedTripTwoOpenDrops() *models.Trip {
	return &models.Trip{
		TripID:   "T-BAT",
		OrderID:  "ORD-A",
		OrderIDs: []string{"ORD-A", "ORD-B"},
		StoreID:  "221",
		DEID:     "de-1",
		DEPhone:  "+260971000001",
		Status:   models.TripStatusOutForDelivery,
		Tasks: []models.Task{
			{TaskID: "pA", Type: models.TaskTypePickup, Status: models.TaskStatusCompleted, OrderID: "ORD-A"},
			{TaskID: "pB", Type: models.TaskTypePickup, Status: models.TaskStatusCompleted, OrderID: "ORD-B"},
			{TaskID: "dA", Type: models.TaskTypeDrop, Status: models.TaskStatusReached, OrderID: "ORD-A", OTP: "1111", Payment: &models.Payment{CollectCash: true, AmountZMW: 20}},
			{TaskID: "dB", Type: models.TaskTypeDrop, Status: models.TaskStatusReached, OrderID: "ORD-B", OTP: "2222", Payment: &models.Payment{CollectCash: true, AmountZMW: 30}},
		},
	}
}

func TestUpdateTaskStatus_InactiveTrip_Conflict(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "T-OLD", OrderID: "ORD-A", DEID: "de-1", Status: models.TripStatusInactive,
		Tasks: []models.Task{{TaskID: "dA", Type: models.TaskTypeDrop, Status: models.TaskStatusReached, OTP: "1111"}},
	}}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001", CurrentTripID: "T-NEW"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	_, err := svc.UpdateTaskStatus(context.Background(), "T-OLD", "dA", "+260971000001", models.TaskStatusCompleted, "1111", "", nil, nil)
	if !errors.Is(err, ErrTripInactive) {
		t.Fatalf("got %v, want ErrTripInactive", err)
	}
}

func TestUpdateTaskStatus_StaleCurrentTripID_Inactive(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "T-OLD", OrderID: "ORD-A", DEID: "de-1", Status: models.TripStatusOutForDelivery,
		Tasks: []models.Task{{TaskID: "dA", Type: models.TaskTypeDrop, Status: models.TaskStatusReached, OTP: "1111"}},
	}}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001", CurrentTripID: "T-NEW"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	_, err := svc.UpdateTaskStatus(context.Background(), "T-OLD", "dA", "+260971000001", models.TaskStatusCompleted, "1111", "", nil, nil)
	if !errors.Is(err, ErrTripInactive) {
		t.Fatalf("got %v, want ErrTripInactive", err)
	}
}

func TestUpdateTaskStatus_IntermediateDrop_KeepsBusyNoPayout(t *testing.T) {
	repo := &stubTripRepo{trip: batchedTripTwoOpenDrops()}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001", CurrentTripID: "T-BAT"}}
	java := &stubJavaOrder{status: "OUT_FOR_DELIVERY"}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = java
	svc.payoutService = nil

	_, err := svc.UpdateTaskStatus(context.Background(), "T-BAT", "dA", "+260971000001", models.TaskStatusCompleted, "1111", "", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !repo.keepBusyCalled {
		t.Fatal("expected CompleteDropKeepBusy")
	}
	if repo.completeTripCalled {
		t.Fatal("must not complete trip while another drop is open")
	}
}

func TestUpdateTaskStatus_LastDrop_CompletesAndPays(t *testing.T) {
	trip := batchedTripTwoOpenDrops()
	trip.Tasks[2].Status = models.TaskStatusCompleted
	repo := &stubTripRepo{trip: trip}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001", CurrentTripID: "T-BAT"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "OUT_FOR_DELIVERY"}

	_, err := svc.UpdateTaskStatus(context.Background(), "T-BAT", "dB", "+260971000001", models.TaskStatusCompleted, "2222", "", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !repo.completeTripCalled {
		t.Fatal("expected CompleteTripAndFreeDE on last drop")
	}
	if repo.keepBusyCalled {
		t.Fatal("last drop must not keep-busy")
	}
}

func TestUpdateTaskStatus_PickupStaysAcceptedWhenOthersOpen(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "T-BAT", OrderID: "ORD-A", OrderIDs: []string{"ORD-A", "ORD-B"}, DEID: "de-1",
		Status: models.TripStatusAccepted,
		Tasks: []models.Task{
			{TaskID: "pA", Type: models.TaskTypePickup, Status: models.TaskStatusCreated, OrderID: "ORD-A"},
			{TaskID: "pB", Type: models.TaskTypePickup, Status: models.TaskStatusCreated, OrderID: "ORD-B"},
			{TaskID: "dA", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OrderID: "ORD-A", OTP: "1111"},
			{TaskID: "dB", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OrderID: "ORD-B", OTP: "2222"},
		},
	}}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001", CurrentTripID: "T-BAT"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}

	_, err := svc.UpdateTaskStatus(context.Background(), "T-BAT", "pA", "+260971000001", models.TaskStatusCompleted, "", "", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.updateStatusStatus == models.TripStatusOutForDelivery {
		t.Fatal("must stay accepted while pickup B is open")
	}
}

func TestRequirePacked_AllowsAlreadyOFD(t *testing.T) {
	svc := &TripService{javaClient: &stubJavaOrder{status: "OUT_FOR_DELIVERY"}, logger: logrus.New()}
	if err := svc.requirePacked(context.Background(), "ORD-A"); err != nil {
		t.Fatalf("already OFD should pass requirePacked: %v", err)
	}
}

func TestAdminCompletePickupByOrder_HitsThatOrder(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "T-BAT", OrderID: "ORD-A", OrderIDs: []string{"ORD-A", "ORD-B"},
		DEID: "de-1", DEPhone: "+260971000001", Status: models.TripStatusAccepted,
		Tasks: []models.Task{
			{TaskID: "pA", Type: models.TaskTypePickup, Status: models.TaskStatusCreated, OrderID: "ORD-A"},
			{TaskID: "pB", Type: models.TaskTypePickup, Status: models.TaskStatusCreated, OrderID: "ORD-B"},
			{TaskID: "dA", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OrderID: "ORD-A"},
			{TaskID: "dB", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OrderID: "ORD-B"},
		},
	}}
	de := &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001", Status: models.DEStatusBusy, CurrentTripID: "T-BAT"}
	svc := newTripServiceForTest(repo, &stubDERepo{de: de}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}

	if err := svc.AdminCompletePickupByOrder(context.Background(), "ORD-B", "ops"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.trip.PickupForOrder("ORD-B").Status != models.TaskStatusCompleted {
		t.Fatal("expected pickup B completed")
	}
	if repo.trip.PickupForOrder("ORD-A").Status != models.TaskStatusCreated {
		t.Fatal("must not complete pickup A")
	}
}

func TestAdminCompleteDropByOrder_HitsThatOrderKeepsTripOpen(t *testing.T) {
	trip := batchedTripTwoOpenDrops()
	trip.Status = models.TripStatusOutForDelivery
	repo := &stubTripRepo{trip: trip}
	de := &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001", Status: models.DEStatusBusy, CurrentTripID: "T-BAT"}
	svc := newTripServiceForTest(repo, &stubDERepo{de: de}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "OUT_FOR_DELIVERY"}

	if err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-B", "ops", "", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.trip.DropForOrder("ORD-B").Status != models.TaskStatusCompleted {
		t.Fatal("expected drop B completed")
	}
	if repo.trip.DropForOrder("ORD-A").Status == models.TaskStatusCompleted {
		t.Fatal("must not complete drop A")
	}
	if repo.completeTripCalled {
		t.Fatal("other drop still open — must not complete trip")
	}
}

func TestCancelTripByOrder_MultiOrder_CancelOne(t *testing.T) {
	live := acceptedSingleton("T-BAT", "ORD-A", "de-1", "+260971000001", -15.41, 28.31)
	live.OrderIDs = []string{"ORD-A", "ORD-B"}
	live.Tasks = append(live.Tasks,
		models.Task{TaskID: "pB", Type: models.TaskTypePickup, Status: models.TaskStatusCreated, OrderID: "ORD-B", Lat: -15.4, Lng: 28.3},
		models.Task{TaskID: "dB", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OrderID: "ORD-B", Lat: -15.42, Lng: 28.32, OTP: "2222"},
	)
	trips := &fakeBatchTrips{
		byID:    map[string]*models.Trip{"T-BAT": live},
		byOrder: map[string]*models.Trip{"ORD-A": live, "ORD-B": live},
	}
	des := &fakeBatchDEs{byPhone: map[string]*models.DeliveryExecutive{
		"+260971000001": {DEID: "de-1", PhoneNumber: "+260971000001", Status: models.DEStatusBusy, CurrentTripID: "T-BAT", CurrentStoreID: "221", LastAppVersion: "1.5.0"},
	}}
	batching := testBatching(trips, des, &fakeDist{}, &captureNotifier{})
	svc := newTripServiceForTest(&stubTripRepo{trip: live}, &stubDERepo{de: des.byPhone["+260971000001"]}, &stubNotifier{})
	svc.SetBatching(batching)

	if err := svc.CancelTripByOrder(context.Background(), "ORD-B", "customer cancel"); err != nil {
		t.Fatalf("cancel-one: %v", err)
	}
	if len(trips.persist) != 1 {
		t.Fatalf("persist calls = %d", len(trips.persist))
	}
	p := trips.persist[0]
	if p.NewTrip == nil {
		t.Fatal("expected leftover successor")
	}
	if p.NewTrip.PickupForOrder("ORD-B") != nil {
		t.Fatal("cancelled order must not remain on successor")
	}
	if p.NewTrip.Status == models.TripStatusCancelled {
		t.Fatal("surviving trip must not be cancelled")
	}
}

func TestGetCurrentTrip_InactiveIsStale(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{TripID: "T-OLD", Status: models.TripStatusInactive, OrderID: "ORD-A"}}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{PhoneNumber: "+2609", CurrentTripID: "T-OLD"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	got, err := svc.GetCurrentTrip(context.Background(), "+2609")
	if err != nil {
		t.Fatalf("GetCurrentTrip: %v", err)
	}
	if got != nil {
		t.Fatalf("inactive current trip must return nil, got %+v", got)
	}
}

func TestBatchedOnTime_UsesPerDropDeadlines(t *testing.T) {
	onTimeDeadline := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC).Unix()
	lateDeadline := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC).Unix()
	ok := &models.Trip{
		OrderIDs: []string{"A", "B"},
		Tasks: []models.Task{
			{Type: models.TaskTypeDrop, CompletedAt: "2026-11-10T11:00:00Z", DropDeadline: &onTimeDeadline},
			{Type: models.TaskTypeDrop, CompletedAt: "2026-11-10T11:30:00Z", DropDeadline: &onTimeDeadline},
		},
	}
	if !ok.DropsOnTime() {
		t.Fatal("expected on-time")
	}
	late := &models.Trip{
		OrderIDs: []string{"A", "B"},
		Tasks: []models.Task{
			{Type: models.TaskTypeDrop, CompletedAt: "2026-11-10T11:00:00Z", DropDeadline: &lateDeadline},
		},
	}
	if late.DropsOnTime() {
		t.Fatal("expected late")
	}
}
