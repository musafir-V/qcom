package service

import (
	"bytes"
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

func TestValidateTaskTransition_CreatedToCompleted(t *testing.T) {
	for _, taskType := range []models.TaskType{models.TaskTypePickup, models.TaskTypeDrop} {
		task := models.Task{Type: taskType, Status: models.TaskStatusCreated}
		if err := validateTaskTransition(task, models.TaskStatusCompleted, false); err != nil {
			t.Fatalf("%s: expected valid transition, got: %v", taskType, err)
		}
	}
}

func TestValidateTaskTransition_LegacyStatusToCompleted(t *testing.T) {
	// Tasks already in legacy intermediate states can still be completed
	// even when require_reached is on (they are not status=created).
	for _, status := range []models.TaskStatus{"arrived", "reached"} {
		task := models.Task{Type: models.TaskTypeDrop, Status: status}
		if err := validateTaskTransition(task, models.TaskStatusCompleted, true); err != nil {
			t.Fatalf("status %q: expected valid transition, got: %v", status, err)
		}
	}
}

func TestValidateTaskTransition_NonCompletedTarget_Rejected(t *testing.T) {
	task := models.Task{Type: models.TaskTypePickup, Status: models.TaskStatusCreated}
	if err := validateTaskTransition(task, models.TaskStatus("arrived"), false); err == nil {
		t.Fatal("expected error: only completed is allowed")
	}
}

func TestValidateTaskTransition_AlreadyCompleted_Rejected(t *testing.T) {
	task := models.Task{Type: models.TaskTypePickup, Status: models.TaskStatusCompleted}
	if err := validateTaskTransition(task, models.TaskStatusCompleted, false); err == nil {
		t.Fatal("expected error: re-entering completed state")
	}
}

func TestValidateTaskTransition_DropCreatedToReached(t *testing.T) {
	task := models.Task{Type: models.TaskTypeDrop, Status: models.TaskStatusCreated}
	if err := validateTaskTransition(task, models.TaskStatusReached, false); err != nil {
		t.Fatalf("expected created→reached, got %v", err)
	}
}

func TestValidateTaskTransition_DropReachedToCompleted(t *testing.T) {
	task := models.Task{Type: models.TaskTypeDrop, Status: models.TaskStatusReached}
	if err := validateTaskTransition(task, models.TaskStatusCompleted, true); err != nil {
		t.Fatalf("expected reached→completed, got %v", err)
	}
}

func TestValidateTaskTransition_DropCreatedToCompleted_AllowedWhenFlagOff(t *testing.T) {
	task := models.Task{Type: models.TaskTypeDrop, Status: models.TaskStatusCreated}
	if err := validateTaskTransition(task, models.TaskStatusCompleted, false); err != nil {
		t.Fatalf("compat path must allow created→completed, got %v", err)
	}
}

func TestValidateTaskTransition_DropCreatedToCompleted_RejectedWhenFlagOn(t *testing.T) {
	task := models.Task{Type: models.TaskTypeDrop, Status: models.TaskStatusCreated}
	err := validateTaskTransition(task, models.TaskStatusCompleted, true)
	if err == nil || !errors.Is(err, ErrDropNotReached) {
		t.Fatalf("got %v, want ErrDropNotReached", err)
	}
}

func TestValidateDropOTP_Correct(t *testing.T) {
	task := models.Task{Type: models.TaskTypeDrop, OTP: "1234"}
	if err := validateDropOTP(task, "1234"); err != nil {
		t.Fatalf("expected valid OTP, got: %v", err)
	}
}

func TestValidateDropOTP_Wrong(t *testing.T) {
	task := models.Task{Type: models.TaskTypeDrop, OTP: "1234"}
	err := validateDropOTP(task, "0000")
	if err == nil {
		t.Fatal("expected error for wrong OTP")
	}
	if !errors.Is(err, ErrInvalidOTP) {
		t.Fatalf("expected ErrInvalidOTP, got: %v", err)
	}
}

func TestValidateDropOTP_Missing(t *testing.T) {
	task := models.Task{Type: models.TaskTypeDrop, OTP: "1234"}
	err := validateDropOTP(task, "")
	if err == nil {
		t.Fatal("expected error for missing OTP")
	}
	if !errors.Is(err, ErrInvalidOTP) {
		t.Fatalf("expected ErrInvalidOTP, got: %v", err)
	}
}

func TestValidateTaskAgainstTripStatus(t *testing.T) {
	cases := []struct {
		name     string
		taskType models.TaskType
		status   models.TripStatus
		wantErr  bool
	}{
		{"pickup allowed when accepted", models.TaskTypePickup, models.TripStatusAccepted, false},
		{"pickup blocked when assigned", models.TaskTypePickup, models.TripStatusAssigned, true},
		{"pickup blocked when created", models.TaskTypePickup, models.TripStatusCreated, true},
		{"drop allowed when out_for_delivery", models.TaskTypeDrop, models.TripStatusOutForDelivery, false},
		{"drop blocked when accepted (pickup not done)", models.TaskTypeDrop, models.TripStatusAccepted, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateTaskAgainstTripStatus(c.taskType, c.status)
			if c.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("expected nil, got %v", err)
			}
			if c.wantErr && err != nil && !errors.Is(err, ErrPrerequisiteIncomplete) {
				t.Fatalf("expected ErrPrerequisiteIncomplete, got %v", err)
			}
		})
	}
}

func TestValidatePickupScan_Match(t *testing.T) {
	trip := &models.Trip{Status: models.TripStatusAccepted, OrderID: "ORD-1"}
	if err := validatePickupScan(trip, "ORD-1"); err != nil {
		t.Fatalf("expected valid scan, got: %v", err)
	}
}

func TestValidatePickupScan_Mismatch(t *testing.T) {
	trip := &models.Trip{Status: models.TripStatusAccepted, OrderID: "ORD-1"}
	if err := validatePickupScan(trip, "ORD-2"); !errors.Is(err, ErrPickupOrderMismatch) {
		t.Fatalf("expected ErrPickupOrderMismatch, got: %v", err)
	}
}

func TestValidatePickupScan_Empty(t *testing.T) {
	trip := &models.Trip{Status: models.TripStatusAccepted, OrderID: "ORD-1"}
	if err := validatePickupScan(trip, ""); !errors.Is(err, ErrPickupOrderMismatch) {
		t.Fatalf("expected ErrPickupOrderMismatch, got: %v", err)
	}
}

func TestValidatePickupScan_WrongState(t *testing.T) {
	trip := &models.Trip{Status: models.TripStatusOutForDelivery, OrderID: "ORD-1"}
	if err := validatePickupScan(trip, "ORD-1"); !errors.Is(err, ErrInvalidTripTransition) {
		t.Fatalf("expected ErrInvalidTripTransition, got: %v", err)
	}
}

func TestCodAccrualAmount(t *testing.T) {
	cases := []struct {
		name string
		trip *models.Trip
		want float64
	}{
		{"nil payment", &models.Trip{}, 0},
		{"online prepaid (no cash to collect)", &models.Trip{
			Payment: &models.Payment{CollectCash: false, AmountZMW: 120},
		}, 0},
		{"COD accrues amount", &models.Trip{
			Payment: &models.Payment{CollectCash: true, AmountZMW: 120},
		}, 120},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := codAccrualAmount(c.trip); got != c.want {
				t.Fatalf("codAccrualAmount = %v, want %v", got, c.want)
			}
		})
	}
}

// --- CancelTripByOrder tests ---

func TestCancelTripByOrder_NoTrip_NoOp(t *testing.T) {
	// GetByOrderID returns nil — no trip created yet; should return nil with no further calls.
	repo := &stubTripRepo{trip: nil}
	notifier := &stubNotifier{}
	svc := newTestTripService(repo, notifier)

	err := svc.CancelTripByOrder(context.Background(), "ORD-001", "customer request")
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if notifier.sent {
		t.Fatal("expected no notification for missing trip")
	}
}

func TestCancelTripByOrder_TripCompleted_SkipNoError(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID:  "T1",
		OrderID: "ORD-001",
		Status:  models.TripStatusCompleted,
		DEID:    "DE-1",
	}}
	notifier := &stubNotifier{}
	svc := newTestTripService(repo, notifier)

	err := svc.CancelTripByOrder(context.Background(), "ORD-001", "admin cancel")
	if err != nil {
		t.Fatalf("expected nil for completed trip, got %v", err)
	}
	if repo.cancelCalled {
		t.Fatal("must not call CancelByOrderID for completed trip")
	}
	if notifier.sent {
		t.Fatal("must not send PN for completed trip")
	}
}

func TestCancelTripByOrder_TripAlreadyCancelled_Idempotent(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID:  "T2",
		OrderID: "ORD-002",
		Status:  models.TripStatusCancelled,
		DEID:    "DE-2",
	}}
	notifier := &stubNotifier{}
	svc := newTestTripService(repo, notifier)

	err := svc.CancelTripByOrder(context.Background(), "ORD-002", "already done")
	if err != nil {
		t.Fatalf("expected nil for already-cancelled trip, got %v", err)
	}
	if repo.cancelCalled {
		t.Fatal("must not call CancelByOrderID for already-cancelled trip")
	}
}

func TestCancelTripByOrder_ActiveAssignedTrip_CancelAndPN(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID:  "T3",
		OrderID: "ORD-003",
		Status:  models.TripStatusAssigned,
		DEID:    "DE-3",
		DEPhone: "+260971000001",
	}}
	notifier := &stubNotifier{}
	svc := newTestTripService(repo, notifier)

	err := svc.CancelTripByOrder(context.Background(), "ORD-003", "out of stock")
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if !repo.cancelCalled {
		t.Fatal("expected CancelByOrderID to be called")
	}
	if repo.cancelTripID != "T3" || repo.cancelDEPhone != "+260971000001" {
		t.Fatalf("wrong cancel args: tripID=%q dePhone=%q", repo.cancelTripID, repo.cancelDEPhone)
	}
	if !notifier.sent {
		t.Fatal("expected PN sent to assigned DE")
	}
	if notifier.lastReq.RecipientID != "DE-3" {
		t.Fatalf("PN sent to wrong recipient: %q", notifier.lastReq.RecipientID)
	}
	if notifier.lastReq.EventType != "TRIP_CANCELLED" {
		t.Fatalf("wrong event type: %q", notifier.lastReq.EventType)
	}
	if notifier.lastReq.Data["type"] != "TRIP_CANCELLED" {
		t.Fatalf("PN data missing type field: %v", notifier.lastReq.Data)
	}
}

func TestCancelTripByOrder_ActiveUnassignedTrip_CancelNoPN(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID:  "T4",
		OrderID: "ORD-004",
		Status:  models.TripStatusCreated,
		DEID:    "",
	}}
	notifier := &stubNotifier{}
	svc := newTestTripService(repo, notifier)

	err := svc.CancelTripByOrder(context.Background(), "ORD-004", "system cancel")
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if !repo.cancelCalled {
		t.Fatal("expected CancelByOrderID for unassigned active trip")
	}
	if notifier.sent {
		t.Fatal("must not send PN when no DE assigned")
	}
}

func TestUpdateTripPayment_NoTrip_NoOp(t *testing.T) {
	repo := &stubTripRepo{trip: nil}
	notifier := &stubNotifier{}
	svc := newTestTripService(repo, notifier)

	res, err := svc.UpdateTripPayment(context.Background(), PaymentUpdateInput{
		OrderID: "ORD-001", PaymentMethod: "AIRTEL_MONEY", GrandTotal: 250,
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Updated || res.Reason != "no_active_trip" {
		t.Fatalf("expected no_active_trip no-op, got %+v", res)
	}
	if repo.updatePaymentCalled {
		t.Fatal("must not call UpdatePayment when no trip exists")
	}
	if notifier.sent {
		t.Fatal("must not push when no trip exists")
	}
}

func TestUpdateTripPayment_ClearsCOD_PushesActiveRider(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "TRIP-1", OrderID: "ORD-002", DEID: "DE-9",
		Status:  models.TripStatusOutForDelivery,
		Payment: &models.Payment{CollectCash: true, AmountZMW: 250, Method: "COD"},
	}}
	notifier := &stubNotifier{}
	svc := newTestTripService(repo, notifier)

	res, err := svc.UpdateTripPayment(context.Background(), PaymentUpdateInput{
		OrderID: "ORD-002", PaymentMethod: "AIRTEL_MONEY", GrandTotal: 250, Currency: "ZMW",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !res.Updated || res.Reason != "" {
		t.Fatalf("expected updated result, got %+v", res)
	}
	if repo.capturedPayment == nil || repo.capturedPayment.CollectCash {
		t.Fatalf("expected collect_cash=false snapshot, got %+v", repo.capturedPayment)
	}
	if repo.capturedPayment.Method != "AIRTEL_MONEY" {
		t.Fatalf("expected method AIRTEL_MONEY, got %q", repo.capturedPayment.Method)
	}
	if !notifier.sent {
		t.Fatal("expected rider push")
	}
	if notifier.lastReq.EventType != "PAYMENT_UPDATED" || notifier.lastReq.Data["collect_cash"] != "false" {
		t.Fatalf("expected PAYMENT_UPDATED push clearing cash, got %+v", notifier.lastReq)
	}
	if notifier.lastReq.Priority != models.PriorityNormal {
		t.Fatalf("expected Normal priority (quiet channel), got %q", notifier.lastReq.Priority)
	}
	if notifier.lastReq.Title == "" || notifier.lastReq.Body == "" {
		t.Fatalf("notifier requires non-empty title/body, got %+v", notifier.lastReq)
	}
}

func TestUpdateTripPayment_TerminalTrip_Rejected(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID: "TRIP-2", OrderID: "ORD-003", DEID: "DE-9",
			Status: models.TripStatusCompleted,
		},
		updatePaymentErr: repository.ErrTripTerminal,
	}
	notifier := &stubNotifier{}
	svc := newTestTripService(repo, notifier)

	res, err := svc.UpdateTripPayment(context.Background(), PaymentUpdateInput{
		OrderID: "ORD-003", PaymentMethod: "AIRTEL_MONEY", GrandTotal: 250,
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Updated || res.Reason != "trip_terminal" {
		t.Fatalf("expected trip_terminal rejection, got %+v", res)
	}
	if notifier.sent {
		t.Fatal("must not push when update rejected")
	}
}

func TestUpdateTripPayment_AssignedTrip_UpdatesNoPush(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "TRIP-3", OrderID: "ORD-005", DEID: "DE-1",
		Status:  models.TripStatusAssigned,
		Payment: &models.Payment{CollectCash: true, AmountZMW: 100, Method: "COD"},
	}}
	notifier := &stubNotifier{}
	svc := newTestTripService(repo, notifier)

	res, err := svc.UpdateTripPayment(context.Background(), PaymentUpdateInput{
		OrderID: "ORD-005", PaymentMethod: "MTN_MONEY", GrandTotal: 100,
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !res.Updated || !repo.updatePaymentCalled {
		t.Fatalf("expected payment updated, got %+v", res)
	}
	// Rider hasn't engaged (assigned, not accepted) — polling backstop covers it.
	if notifier.sent {
		t.Fatal("must not push for a non-active (assigned) trip")
	}
}

func TestUpdateTripPayment_ActiveNoCashChange_NoPush(t *testing.T) {
	// Switching between two online methods on an active trip: collect_cash stays
	// false, amount unchanged — nothing rider-relevant changed, so no push.
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "TRIP-4", OrderID: "ORD-006", DEID: "DE-2",
		Status:  models.TripStatusOutForDelivery,
		Payment: &models.Payment{CollectCash: false, AmountZMW: 100, Method: "AIRTEL_MONEY"},
	}}
	notifier := &stubNotifier{}
	svc := newTestTripService(repo, notifier)

	res, err := svc.UpdateTripPayment(context.Background(), PaymentUpdateInput{
		OrderID: "ORD-006", PaymentMethod: "MTN_MONEY", GrandTotal: 100,
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !res.Updated {
		t.Fatalf("expected updated, got %+v", res)
	}
	if notifier.sent {
		t.Fatal("must not push when cash requirement did not change")
	}
}

func TestEditTripByOrder_NoTripIsNoop(t *testing.T) {
	repo := &stubTripRepo{trip: nil}
	svc := newTestTripService(repo, &stubNotifier{})

	res, err := svc.EditTripByOrder(context.Background(), EditTripByOrderInput{
		OrderID: "ORD-EDIT-1", PaymentMethod: "COD", GrandTotal: 100, Currency: "ZMW",
		DeliveryZone: "Blue Rack 2",
		Items:        []EditTripItemInput{{SKU: "SKU-1", Name: "Milk", ImageURL: "http://img/1", Quantity: 2}},
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Updated || res.Reason != "no_active_trip" {
		t.Fatalf("expected no_active_trip no-op, got %+v", res)
	}
	if repo.updateEditByOrderCalled {
		t.Fatal("must not call UpdateEditByOrder when no trip exists")
	}
}

func TestEditTripByOrder_OverwritesItemsPaymentAndPickupZone(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "TRIP-EDIT-1", OrderID: "ORD-EDIT-2",
		Status:  models.TripStatusAssigned,
		Payment: &models.Payment{CollectCash: true, AmountZMW: 50, Method: "COD"},
		Items:   []models.TripItem{{Sku: "OLD", Name: "Old", Quantity: 1}},
		Tasks: []models.Task{
			{TaskID: "t-pickup", Type: models.TaskTypePickup, DeliveryZone: "Old Zone"},
			{TaskID: "t-drop", Type: models.TaskTypeDrop, OTP: "1234"},
		},
	}}
	svc := newTestTripService(repo, &stubNotifier{})

	res, err := svc.EditTripByOrder(context.Background(), EditTripByOrderInput{
		OrderID: "ORD-EDIT-2", PaymentMethod: "AIRTEL_MONEY", GrandTotal: 275.5, Currency: "ZMW",
		DeliveryZone: "Blue Rack 2",
		Items: []EditTripItemInput{
			{SKU: "SKU-A", Name: "Bread", ImageURL: "http://img/a", Quantity: 3},
			{SKU: "SKU-B", Name: "Eggs", ImageURL: "http://img/b", Quantity: 1},
		},
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !res.Updated || res.Reason != "" {
		t.Fatalf("expected updated result, got %+v", res)
	}
	if !repo.updateEditByOrderCalled {
		t.Fatal("expected UpdateEditByOrder to be called")
	}
	if len(repo.editedItems) != 2 {
		t.Fatalf("expected 2 items, got %+v", repo.editedItems)
	}
	if repo.editedItems[0].Sku != "SKU-A" || repo.editedItems[0].Name != "Bread" ||
		repo.editedItems[0].ImageURL != "http://img/a" || repo.editedItems[0].Quantity != 3 {
		t.Fatalf("unexpected first item: %+v", repo.editedItems[0])
	}
	if repo.editedItems[1].Sku != "SKU-B" || repo.editedItems[1].Quantity != 1 {
		t.Fatalf("unexpected second item: %+v", repo.editedItems[1])
	}
	if repo.editedPayment == nil || repo.editedPayment.CollectCash ||
		repo.editedPayment.AmountZMW != 275.5 || repo.editedPayment.Method != "AIRTEL_MONEY" ||
		repo.editedPayment.Currency != "ZMW" {
		t.Fatalf("expected paymentFromOrder snapshot, got %+v", repo.editedPayment)
	}
	if len(repo.editedTasks) != 2 {
		t.Fatalf("expected 2 tasks, got %+v", repo.editedTasks)
	}
	if repo.editedTasks[0].Type != models.TaskTypePickup || repo.editedTasks[0].DeliveryZone != "Blue Rack 2" {
		t.Fatalf("expected pickup DeliveryZone updated, got %+v", repo.editedTasks[0])
	}
	if repo.editedTasks[1].Type != models.TaskTypeDrop || repo.editedTasks[1].OTP != "1234" {
		t.Fatalf("drop task must be preserved, got %+v", repo.editedTasks[1])
	}
}

func TestEditTripByOrder_IdempotentSecondCall(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "TRIP-EDIT-2", OrderID: "ORD-EDIT-3",
		Status: models.TripStatusAccepted,
		Tasks: []models.Task{
			{TaskID: "t-pickup", Type: models.TaskTypePickup, DeliveryZone: "Zone A"},
		},
	}}
	svc := newTestTripService(repo, &stubNotifier{})
	in := EditTripByOrderInput{
		OrderID: "ORD-EDIT-3", PaymentMethod: "COD", GrandTotal: 90, Currency: "ZMW",
		DeliveryZone: "Zone B",
		Items:        []EditTripItemInput{{SKU: "SKU-1", Name: "Milk", Quantity: 2}},
	}

	res1, err := svc.EditTripByOrder(context.Background(), in)
	if err != nil || !res1.Updated {
		t.Fatalf("first call: res=%+v err=%v", res1, err)
	}
	firstItems := append([]models.TripItem(nil), repo.editedItems...)
	var firstPayment models.Payment
	if repo.editedPayment != nil {
		firstPayment = *repo.editedPayment
	}
	firstTasks := append([]models.Task(nil), repo.editedTasks...)

	res2, err := svc.EditTripByOrder(context.Background(), in)
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	if !res2.Updated || res2.Reason != "" {
		t.Fatalf("second identical call must succeed, got %+v", res2)
	}
	if len(repo.editedItems) != len(firstItems) {
		t.Fatalf("second call items len = %d, first = %d", len(repo.editedItems), len(firstItems))
	}
	for i := range firstItems {
		if repo.editedItems[i] != firstItems[i] {
			t.Fatalf("second call item[%d] = %+v, first = %+v", i, repo.editedItems[i], firstItems[i])
		}
	}
	if repo.editedPayment == nil {
		t.Fatal("second call payment is nil")
	}
	if *repo.editedPayment != firstPayment {
		t.Fatalf("second call payment = %+v, first = %+v", *repo.editedPayment, firstPayment)
	}
	if len(repo.editedTasks) != len(firstTasks) {
		t.Fatalf("second call tasks len = %d, first = %d", len(repo.editedTasks), len(firstTasks))
	}
	for i := range firstTasks {
		if repo.editedTasks[i] != firstTasks[i] {
			t.Fatalf("second call task[%d] = %+v, first = %+v", i, repo.editedTasks[i], firstTasks[i])
		}
	}
}

func TestEditTripByOrder_TerminalTrip_Rejected(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID: "TRIP-EDIT-3", OrderID: "ORD-EDIT-4",
			Status: models.TripStatusCompleted,
			Tasks:  []models.Task{{TaskID: "t-pickup", Type: models.TaskTypePickup}},
		},
		updateEditByOrderErr: repository.ErrTripTerminal,
	}
	svc := newTestTripService(repo, &stubNotifier{})

	res, err := svc.EditTripByOrder(context.Background(), EditTripByOrderInput{
		OrderID: "ORD-EDIT-4", PaymentMethod: "COD", GrandTotal: 10, Currency: "ZMW",
		DeliveryZone: "Zone X",
		Items:        []EditTripItemInput{{SKU: "SKU-1", Name: "X", Quantity: 1}},
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if res.Updated || res.Reason != "trip_terminal" {
		t.Fatalf("expected trip_terminal rejection, got %+v", res)
	}
}

// --- test helpers ---

// stubTripRepo satisfies tripRepoI. GetByOrderID and CancelByOrderID are used by
// CancelTripByOrder. GetByID and CompleteTripAndFreeDE are used by UpdateTaskStatus
// (drop path). updateTasksFn, if set, is called by UpdateTasks (pickup path).
type stubTripRepo struct {
	trip                    *models.Trip
	cancelCalled            bool
	cancelTripID            string
	cancelDEPhone           string
	updateTasksFn           func(ctx context.Context, tripID string, tasks []models.Task) error
	updateTasksCalled       bool
	capturedTasks           []models.Task
	completeTripCalled      bool
	updatePaymentCalled     bool
	updatePaymentErr        error
	capturedPayment         *models.Payment
	updateEditByOrderCalled bool
	updateEditByOrderErr    error
	editedItems             []models.TripItem
	editedPayment           *models.Payment
	editedTasks             []models.Task
	updateStatusCalled      bool
	updateStatusTripID      string
	updateStatusStatus      models.TripStatus
	dropDeadline            int64
	adminAssignCalled       bool
	adminAssignErr          error
}

func (s *stubTripRepo) GetByOrderID(_ context.Context, _ string) (*models.Trip, error) {
	return s.trip, nil
}

func (s *stubTripRepo) CancelByOrderID(_ context.Context, tripID, dePhone, _ string) error {
	s.cancelCalled = true
	s.cancelTripID = tripID
	s.cancelDEPhone = dePhone
	return nil
}

func (s *stubTripRepo) GetByID(_ context.Context, _ string) (*models.Trip, error) {
	return s.trip, nil
}

func (s *stubTripRepo) CompleteTripAndFreeDE(_ context.Context, _, _, _ string, tasks []models.Task, _ float64) error {
	s.completeTripCalled = true
	s.capturedTasks = make([]models.Task, len(tasks))
	copy(s.capturedTasks, tasks)
	return nil
}

func (s *stubTripRepo) UpdateTasks(ctx context.Context, tripID string, tasks []models.Task) error {
	s.updateTasksCalled = true
	if s.trip != nil {
		s.trip.Tasks = make([]models.Task, len(tasks))
		copy(s.trip.Tasks, tasks)
	}
	if s.updateTasksFn != nil {
		return s.updateTasksFn(ctx, tripID, tasks)
	}
	return nil
}

func (s *stubTripRepo) UpdateStatus(_ context.Context, tripID string, status models.TripStatus) error {
	s.updateStatusCalled = true
	s.updateStatusTripID = tripID
	s.updateStatusStatus = status
	return nil
}

func (s *stubTripRepo) MarkOutForDelivery(_ context.Context, tripID string, dropDeadline int64) error {
	s.updateStatusCalled = true
	s.updateStatusTripID = tripID
	s.updateStatusStatus = models.TripStatusOutForDelivery
	s.dropDeadline = dropDeadline
	return nil
}

type stubDropDeadlineConfigStore struct {
	cfg    *models.DropDeadlineConfig
	getErr error
}

func (s *stubDropDeadlineConfigStore) Get(_ context.Context) (*models.DropDeadlineConfig, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.cfg == nil {
		return &models.DropDeadlineConfig{}, nil
	}
	return s.cfg, nil
}

func (s *stubTripRepo) AdminAssign(_ context.Context, _, _, deID, dePhone, _ string) error {
	s.adminAssignCalled = true
	if s.adminAssignErr != nil {
		return s.adminAssignErr
	}
	if s.trip != nil {
		s.trip.Status = models.TripStatusAccepted
		s.trip.DEID = deID
		s.trip.DEPhone = dePhone
	}
	return nil
}

func (s *stubTripRepo) Accept(_ context.Context, _, _ string) error {
	if s.trip != nil {
		s.trip.Status = models.TripStatusAccepted
	}
	return nil
}

func (s *stubTripRepo) RejectToPool(_ context.Context, _, _, _, _ string) error {
	panic("stubTripRepo.RejectToPool: unexpected call")
}

func (s *stubTripRepo) UpdatePayment(_ context.Context, _ string, payment *models.Payment) error {
	s.updatePaymentCalled = true
	s.capturedPayment = payment
	return s.updatePaymentErr
}

func (s *stubTripRepo) UpdateEditByOrder(_ context.Context, _ string, items []models.TripItem, payment *models.Payment, tasks []models.Task) error {
	s.updateEditByOrderCalled = true
	s.editedItems = items
	s.editedPayment = payment
	s.editedTasks = tasks
	return s.updateEditByOrderErr
}

// stubDERepo satisfies deRepoI for unit tests.
type stubDERepo struct {
	de          *models.DeliveryExecutive
	byPhone     map[string]*models.DeliveryExecutive
	listed      []*models.DeliveryExecutive
	statusCalls []string
	attachCalls int
}

func (s *stubDERepo) GetByPhone(_ context.Context, phone string) (*models.DeliveryExecutive, error) {
	if s.byPhone != nil {
		return s.byPhone[phone], nil
	}
	if s.de != nil && (phone == "" || phone == s.de.PhoneNumber) {
		return s.de, nil
	}
	return s.de, nil
}

func (s *stubDERepo) UpdateStatus(_ context.Context, phone string, status models.DEStatus, _, _ string) error {
	s.statusCalls = append(s.statusCalls, phone+":"+string(status))
	if de, err := s.GetByPhone(context.Background(), phone); err == nil && de != nil {
		de.Status = status
	}
	return nil
}

func (s *stubDERepo) AttachToTrip(_ context.Context, phone, orderID, tripID, _ string) error {
	s.attachCalls++
	if de, err := s.GetByPhone(context.Background(), phone); err == nil && de != nil {
		de.Status = models.DEStatusBusy
		de.CurrentOrderID = orderID
		de.CurrentTripID = tripID
	}
	return nil
}

func (s *stubDERepo) ListByAssignedStore(_ context.Context, _, _, _ string, _ int32) ([]*models.DeliveryExecutive, string, error) {
	return s.listed, "", nil
}

type stubJavaOrder struct {
	status    string
	getErr    error
	updates   []string
	updateErr error
}

func (s *stubJavaOrder) GetOrderStatus(_ context.Context, _ string) (string, error) {
	if s.getErr != nil {
		return "", s.getErr
	}
	return s.status, nil
}

func (s *stubJavaOrder) UpdateOrderStatus(_ context.Context, _, status, _ string) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	s.updates = append(s.updates, status)
	return nil
}

// stubNotifier satisfies NotificationService. Guarded by a mutex because
// notifyCustomer dispatches sends on a goroutine.
type stubNotifier struct {
	mu      sync.Mutex
	sent    bool
	lastReq models.NotificationSendRequest
	result  models.NotificationSendResult
}

func (s *stubNotifier) Send(_ context.Context, req models.NotificationSendRequest) models.NotificationSendResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = true
	s.lastReq = req
	if s.result.Status == "" {
		return models.NotificationSendResult{Status: models.SendStatusSent}
	}
	return s.result
}

func (s *stubNotifier) UpsertDeviceToken(_ context.Context, _ models.RecipientType, _, _, _ string) error {
	return nil
}

func (s *stubNotifier) ClearDeviceToken(_ context.Context, _ models.RecipientType, _ string) error {
	return nil
}

func newTestTripService(repo *stubTripRepo, notifier *stubNotifier) *TripService {
	return &TripService{
		tripRepo: repo,
		notifier: notifier,
		logger:   logrus.New(),
	}
}

// newTripServiceForTest creates a TripService wired with stubs for trip and DE
// repos, suitable for testing UpdateTaskStatus paths. notifier is wired in too
// so the full applyTaskCompletion funnel — including the customer push — is
// exercised, not just the layer below it.
func newTripServiceForTest(repo *stubTripRepo, deRepo *stubDERepo, notifier *stubNotifier) *TripService {
	return &TripService{
		tripRepo: repo,
		deRepo:   deRepo,
		notifier: notifier,
		logger:   logrus.New(),
	}
}

func TestUpdateTaskStatus_PhotoS3Key_StoredOnDrop(t *testing.T) {
	// Verify that when photoS3Key is passed, it is stored on the drop task before
	// CompleteTripAndFreeDE is called. Drop tasks route through applyTaskCompletion →
	// CompleteTripAndFreeDE (not UpdateTasks), so we capture tasks there.
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:  "t1",
			OrderID: "ORD-001",
			DEID:    "de-1",
			Status:  models.TripStatusOutForDelivery,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCompleted, CompletedAt: "2026-06-24T10:00:00Z"},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})

	_, err := svc.UpdateTaskStatus(context.Background(), "t1", "task-drop", "+260971000001", models.TaskStatusCompleted, "1234", "orders/ORD-001/drop/de-1/abc.jpg", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var dropTask *models.Task
	for i := range repo.capturedTasks {
		if repo.capturedTasks[i].Type == models.TaskTypeDrop {
			dropTask = &repo.capturedTasks[i]
			break
		}
	}
	if dropTask == nil {
		t.Fatal("CompleteTripAndFreeDE was not called or no drop task in captured tasks")
	}
	const wantKey = "orders/ORD-001/drop/de-1/abc.jpg"
	if dropTask.PhotoS3Key != wantKey {
		t.Errorf("drop task PhotoS3Key = %q, want %q", dropTask.PhotoS3Key, wantKey)
	}
}

// TestUpdateTaskStatus_DropCompletion_NotifiesCustomer pins the full
// production funnel for the drop path: driver completes the drop task via
// UpdateTaskStatus → applyTaskCompletion → notifyCustomer → a customer push
// actually goes out. Every earlier notifyCustomer test called that method
// directly, which is not the layer that was broken in production — the
// dead-on-arrival bug lived in the call site inside applyTaskCompletion, and
// no test exercised it. This test would fail if that call site were deleted.
func TestUpdateTaskStatus_DropCompletion_NotifiesCustomer(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:         "t1",
			OrderID:        "ORD-DROP-1",
			DEID:           "de-1",
			CustomerUserID: "US-CUSTOMER-DROP",
			Status:         models.TripStatusOutForDelivery,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCompleted, CompletedAt: "2026-06-24T10:00:00Z"},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001", Name: "Chanda"}}
	notifier := &stubNotifier{}
	svc := newTripServiceForTest(repo, deRepo, notifier)

	_, err := svc.UpdateTaskStatus(context.Background(), "t1", "task-drop", "+260971000001", models.TaskStatusCompleted, "1234", "", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !waitForSend(notifier) {
		t.Fatal("expected a customer push to be sent when the drop task completes")
	}
	if notifier.lastReq.EventType != "ORDER_DELIVERED" {
		t.Fatalf("event type = %q, want ORDER_DELIVERED", notifier.lastReq.EventType)
	}
	if notifier.lastReq.RecipientID != "US-CUSTOMER-DROP" {
		t.Fatalf("recipient id = %q, want %q", notifier.lastReq.RecipientID, "US-CUSTOMER-DROP")
	}
}

// TestUpdateTaskStatus_PickupCompletion_NotifiesCustomer is the pickup-path
// mirror of TestUpdateTaskStatus_DropCompletion_NotifiesCustomer: driver
// completes pickup via UpdateTaskStatus → applyTaskCompletion →
// onTaskCompleted → notifyCustomer. This would fail if that call site were
// deleted.
func TestUpdateTaskStatus_PickupCompletion_NotifiesCustomer(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:         "t2",
			OrderID:        "ORD-PICKUP-1",
			DEID:           "de-1",
			CustomerUserID: "US-CUSTOMER-PICKUP",
			Status:         models.TripStatusAccepted,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001", Name: "Chanda"}}
	notifier := &stubNotifier{}
	svc := newTripServiceForTest(repo, deRepo, notifier)
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}

	_, err := svc.UpdateTaskStatus(context.Background(), "t2", "task-pickup", "+260971000001", models.TaskStatusCompleted, "", "", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !waitForSend(notifier) {
		t.Fatal("expected a customer push to be sent when the pickup task completes")
	}
	if notifier.lastReq.EventType != "ORDER_OUT_FOR_DELIVERY" {
		t.Fatalf("event type = %q, want ORDER_OUT_FOR_DELIVERY", notifier.lastReq.EventType)
	}
	if notifier.lastReq.RecipientID != "US-CUSTOMER-PICKUP" {
		t.Fatalf("recipient id = %q, want %q", notifier.lastReq.RecipientID, "US-CUSTOMER-PICKUP")
	}
	if !repo.updateStatusCalled || repo.updateStatusStatus != models.TripStatusOutForDelivery {
		t.Fatalf("expected trip status mirrored to out_for_delivery, got called=%v status=%q", repo.updateStatusCalled, repo.updateStatusStatus)
	}
}

func TestUpdateTaskStatus_PickupCompletion_FreezesDropDeadline(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:     "t2",
			OrderID:    "ORD-PICKUP-1",
			DEID:       "de-1",
			Status:     models.TripStatusAccepted,
			DistanceKM: 3.2,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}

	before := timezone.Now()
	_, err := svc.UpdateTaskStatus(context.Background(), "t2", "task-pickup", "+260971000001", models.TaskStatusCompleted, "", "", nil, nil)
	after := timezone.Now()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantLo := models.ComputeDropDeadlineUnix(before, 3.2, 2, 0)
	wantHi := models.ComputeDropDeadlineUnix(after, 3.2, 2, 0)
	if repo.dropDeadline < wantLo || repo.dropDeadline > wantHi {
		t.Fatalf("dropDeadline = %d, want [%d, %d] (3.2km * 2 + 0 = 384s)", repo.dropDeadline, wantLo, wantHi)
	}
}

func TestUpdateTaskStatus_PickupCompletion_UsesConfigXY(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:     "t3",
			OrderID:    "ORD-PICKUP-XY",
			DEID:       "de-1",
			Status:     models.TripStatusAccepted,
			DistanceKM: 2,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	svc.dropDeadlineConfig = &stubDropDeadlineConfigStore{
		cfg: &models.DropDeadlineConfig{MinutesPerKm: 3, ExtraMinutes: 4},
	}

	before := timezone.Now()
	_, err := svc.UpdateTaskStatus(context.Background(), "t3", "task-pickup", "+260971000001", models.TaskStatusCompleted, "", "", nil, nil)
	after := timezone.Now()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantLo := models.ComputeDropDeadlineUnix(before, 2, 3, 4)
	wantHi := models.ComputeDropDeadlineUnix(after, 2, 3, 4)
	if repo.dropDeadline < wantLo || repo.dropDeadline > wantHi {
		t.Fatalf("dropDeadline = %d, want [%d, %d] (~now+600s)", repo.dropDeadline, wantLo, wantHi)
	}
}

func TestVerifyPickup_BlockedUntilReadyForDelivery(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:  "t-verify-pack",
			OrderID: "ORD-VERIFY-PACK",
			DEID:    "de-1",
			Status:  models.TripStatusAccepted,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "PACKING"}

	err := svc.VerifyPickup(context.Background(), "t-verify-pack", "+260971000001", "ORD-VERIFY-PACK")
	if !errors.Is(err, ErrOrderNotPacked) {
		t.Fatalf("VerifyPickup error = %v, want ErrOrderNotPacked", err)
	}
}

func TestVerifyPickup_AllowedWhenRFD(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:  "t-verify-rfd",
			OrderID: "ORD-VERIFY-RFD",
			DEID:    "de-1",
			Status:  models.TripStatusAccepted,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}

	err := svc.VerifyPickup(context.Background(), "t-verify-rfd", "+260971000001", "ORD-VERIFY-RFD")
	if err != nil {
		t.Fatalf("VerifyPickup error = %v, want nil", err)
	}
}

func TestUpdateTaskStatus_PickupComplete_BlockedUntilReadyForDelivery(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:  "t-pickup-block",
			OrderID: "ORD-PICKUP-BLOCK",
			DEID:    "de-1",
			Status:  models.TripStatusAccepted,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "CONFIRMED"}

	_, err := svc.UpdateTaskStatus(context.Background(), "t-pickup-block", "task-pickup", "+260971000001", models.TaskStatusCompleted, "", "", nil, nil)
	if !errors.Is(err, ErrOrderNotPacked) {
		t.Fatalf("PickupComplete error = %v, want ErrOrderNotPacked", err)
	}
	if repo.updateStatusCalled {
		t.Fatal("must not write OFD when order is not packed")
	}
	if repo.dropDeadline != 0 {
		t.Fatalf("dropDeadline = %d, want 0 (no OFD write)", repo.dropDeadline)
	}
	if repo.updateTasksCalled {
		t.Fatal("must not complete pickup task when order is not packed")
	}
}

func TestUpdateTaskStatus_PickupComplete_JavaStatusError(t *testing.T) {
	javaErr := errors.New("boom")
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:  "t-pickup-java-err",
			OrderID: "ORD-PICKUP-JAVA-ERR",
			DEID:    "de-1",
			Status:  models.TripStatusAccepted,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{getErr: javaErr}

	_, err := svc.UpdateTaskStatus(context.Background(), "t-pickup-java-err", "task-pickup", "+260971000001", models.TaskStatusCompleted, "", "", nil, nil)
	if !errors.Is(err, javaErr) {
		t.Fatalf("PickupComplete error = %v, want java status error %v", err, javaErr)
	}
	if errors.Is(err, ErrOrderNotPacked) {
		t.Fatal("java status error must not be treated as not-packed")
	}
	if repo.updateStatusCalled {
		t.Fatal("must not write OFD when java status lookup fails")
	}
	if repo.dropDeadline != 0 {
		t.Fatalf("dropDeadline = %d, want 0 (no OFD write)", repo.dropDeadline)
	}
	if repo.updateTasksCalled {
		t.Fatal("must not complete pickup task when java status lookup fails")
	}
}

func TestUpdateTaskStatus_PickupComplete_NilJavaClientFailsClosed(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:  "t-pickup-no-java",
			OrderID: "ORD-PICKUP-NO-JAVA",
			DEID:    "de-1",
			Status:  models.TripStatusAccepted,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})

	_, err := svc.UpdateTaskStatus(context.Background(), "t-pickup-no-java", "task-pickup", "+260971000001", models.TaskStatusCompleted, "", "", nil, nil)
	if !errors.Is(err, ErrOrderNotPacked) {
		t.Fatalf("PickupComplete error = %v, want ErrOrderNotPacked when java client absent", err)
	}
	if repo.updateStatusCalled || repo.updateTasksCalled {
		t.Fatal("must not mutate trip when java client is nil")
	}
}

func TestUpdateTaskStatus_PickupComplete_AllowedWhenRFD(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:  "t-pickup-rfd",
			OrderID: "ORD-PICKUP-RFD",
			DEID:    "de-1",
			Status:  models.TripStatusAccepted,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}

	result, err := svc.UpdateTaskStatus(context.Background(), "t-pickup-rfd", "task-pickup", "+260971000001", models.TaskStatusCompleted, "", "", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || result.Status != "updated" {
		t.Fatalf("result = %+v, want status updated", result)
	}
	if !repo.updateStatusCalled || repo.updateStatusStatus != models.TripStatusOutForDelivery {
		t.Fatalf("expected OFD write, got called=%v status=%q", repo.updateStatusCalled, repo.updateStatusStatus)
	}
}

func TestAcceptTrip_NotGatedOnPacked(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:  "t-accept-pack",
			OrderID: "ORD-ACCEPT-PACK",
			DEID:    "de-1",
			Status:  models.TripStatusAssigned,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000001"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "PACKING"}

	if err := svc.AcceptTrip(context.Background(), "t-accept-pack", "+260971000001"); err != nil {
		t.Fatalf("AcceptTrip must not be packed-gated, got: %v", err)
	}
	if repo.trip.Status != models.TripStatusAccepted {
		t.Fatalf("trip status = %q, want accepted", repo.trip.Status)
	}
}

func TestGetCurrentTrip_ReturnsStoredDropDeadlineNotRecomputed(t *testing.T) {
	stored := int64(1700000000)
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:       "t1",
			OrderID:      "ORD-1",
			Status:       models.TripStatusOutForDelivery,
			DistanceKM:   1,
			DropDeadline: &stored,
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{
		DEID:           "de-1",
		PhoneNumber:    "+260971000001",
		CurrentOrderID: "ORD-1",
	}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.dropDeadlineConfig = &stubDropDeadlineConfigStore{
		cfg: &models.DropDeadlineConfig{MinutesPerKm: 99, ExtraMinutes: 99},
	}

	got, err := svc.GetCurrentTrip(context.Background(), "+260971000001")
	if err != nil {
		t.Fatalf("GetCurrentTrip: %v", err)
	}
	if got == nil || got.DropDeadline == nil {
		t.Fatal("expected stored drop_deadline")
	}
	if *got.DropDeadline != stored {
		t.Fatalf("drop_deadline = %d, want stored %d (must not recompute)", *got.DropDeadline, stored)
	}
}

func TestNotifyCustomer_SendsBuiltRequest(t *testing.T) {
	notifier := &stubNotifier{}
	svc := newTestTripService(&stubTripRepo{}, notifier)

	trip := &models.Trip{OrderID: "ORD1289752277", CustomerUserID: "US0418437320"}
	de := &models.DeliveryExecutive{Name: "Chanda"}

	// notifyCustomer dispatches asynchronously; waitForSend polls so the test
	// does not depend on goroutine scheduling.
	svc.notifyCustomer(trip, de, eventOutForDelivery)

	if !waitForSend(notifier) {
		t.Fatal("expected a notification to be sent")
	}
	if notifier.lastReq.RecipientID != "US0418437320" {
		t.Fatalf("recipient id = %q", notifier.lastReq.RecipientID)
	}
	if notifier.lastReq.EventType != "ORDER_OUT_FOR_DELIVERY" {
		t.Fatalf("event type = %q", notifier.lastReq.EventType)
	}
	if notifier.lastReq.Data["screen"] != "ORDER_DETAILS_SCREEN" {
		t.Fatalf("screen = %q", notifier.lastReq.Data["screen"])
	}
}

func TestNotifyCustomer_NoCustomerIDDoesNotSend(t *testing.T) {
	notifier := &stubNotifier{}
	svc := newTestTripService(&stubTripRepo{}, notifier)

	svc.notifyCustomer(&models.Trip{OrderID: "ORD1849915231"}, &models.DeliveryExecutive{Name: "Chanda"}, eventDelivered)

	if waitForSend(notifier) {
		t.Fatal("expected no notification when CustomerUserID is blank")
	}
}

func TestNotifyCustomer_NilNotifierDoesNotPanic(t *testing.T) {
	svc := newTestTripService(&stubTripRepo{}, &stubNotifier{})
	// Assign untyped nil directly. Passing a nil *stubNotifier into
	// newTestTripService would store a typed nil in the interface field, so
	// `s.notifier == nil` would be false and the guard would not fire.
	svc.notifier = nil

	// Must not panic.
	svc.notifyCustomer(
		&models.Trip{OrderID: "ORD1289752277", CustomerUserID: "US0418437320"},
		&models.DeliveryExecutive{Name: "Chanda"},
		eventDelivered,
	)
}

// waitForSend polls the stub for up to ~500ms. Returns true once a send has
// been recorded, false if none arrives. Used because notifyCustomer dispatches
// on a goroutine.
func waitForSend(n *stubNotifier) bool {
	for i := 0; i < 50; i++ {
		n.mu.Lock()
		sent := n.sent
		n.mu.Unlock()
		if sent {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// safeBuf is a mutex-guarded io.Writer for capturing log output written from
// the notify goroutine. A bare bytes.Buffer would data-race under -race:
// logrus writes from the goroutine while the test reads from the test
// goroutine.
type safeBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// waitForLog polls the captured output for a substring, up to ~1s.
// Polling, not a fixed sleep: the log line is written after Send returns, so
// waitForSend has already returned by then and cannot be used to synchronise.
func waitForLog(w *safeBuf, want string) bool {
	for i := 0; i < 100; i++ {
		if strings.Contains(w.String(), want) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestNotifyCustomer_LogsSkippedOutcome(t *testing.T) {
	notifier := &stubNotifier{result: models.NotificationSendResult{
		Status: models.SendStatusSkipped,
		Reason: "no_token",
	}}
	out := &safeBuf{}
	logger := logrus.New()
	logger.SetOutput(out)
	logger.SetLevel(logrus.InfoLevel)

	svc := newTestTripService(&stubTripRepo{}, notifier)
	svc.logger = logger

	svc.notifyCustomer(
		&models.Trip{OrderID: "ORD1289752277", CustomerUserID: "US0418437320"},
		&models.DeliveryExecutive{Name: "Chanda"},
		eventDelivered,
	)

	// Wait on the last field to be written, then assert the rest are present.
	if !waitForLog(out, "no_token") {
		t.Fatalf("timed out waiting for the outcome log; got: %s", out.String())
	}
	got := out.String()
	for _, want := range []string{"ORDER_DELIVERED", "ORD1289752277", "skipped", "no_token"} {
		if !strings.Contains(got, want) {
			t.Fatalf("log missing %q; got: %s", want, got)
		}
	}
}

// --- Admin drop-complete (no OTP) tests ---

func TestJavaActor_RiderVsAdmin(t *testing.T) {
	de := &models.DeliveryExecutive{DEID: "de-1"}
	if got := javaActor(de, ""); got != "DE:de-1" {
		t.Fatalf("javaActor(rider) = %q, want %q", got, "DE:de-1")
	}
	if got := javaActor(de, "shivang"); got != "ADMIN:shivang" {
		t.Fatalf("javaActor(admin) = %q, want %q", got, "ADMIN:shivang")
	}
}

// TestAdminCompleteTask_Drop_SkipsOTP pins the requirement that admin-driven
// drop completion never checks the customer OTP: the drop task below carries
// a real OTP, AdminCompleteTask is called with no OTP at all (the signature
// no longer accepts one), and completion still succeeds.
func TestAdminCompleteTask_Drop_SkipsOTP(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:  "t1",
			OrderID: "ORD-ADMIN-1",
			DEID:    "de-1",
			DEPhone: "+260971000001",
			Status:  models.TripStatusOutForDelivery,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCompleted},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{
		DEID: "de-1", PhoneNumber: "+260971000001", CurrentOrderID: "ORD-ADMIN-1",
	}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})

	err := svc.AdminCompleteTask(context.Background(), "+260971000001", models.TaskTypeDrop, "some-admin")
	if err != nil {
		t.Fatalf("expected admin drop to succeed without OTP, got: %v", err)
	}

	var dropTask *models.Task
	for i := range repo.capturedTasks {
		if repo.capturedTasks[i].Type == models.TaskTypeDrop {
			dropTask = &repo.capturedTasks[i]
			break
		}
	}
	if dropTask == nil || dropTask.Status != models.TaskStatusCompleted {
		t.Fatalf("expected drop task completed via CompleteTripAndFreeDE, got: %+v", repo.capturedTasks)
	}
}

// TestAdminCompleteTask_Drop_RequiresOutForDelivery pins the trip-status gate:
// admin completion still hard-fails when pickup has not been done yet.
func TestAdminCompleteTask_Drop_RequiresOutForDelivery(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:  "t2",
			OrderID: "ORD-ADMIN-2",
			DEID:    "de-1",
			DEPhone: "+260971000002",
			Status:  models.TripStatusAccepted, // pickup not done yet
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{
		DEID: "de-1", PhoneNumber: "+260971000002", CurrentOrderID: "ORD-ADMIN-2",
	}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})

	err := svc.AdminCompleteTask(context.Background(), "+260971000002", models.TaskTypeDrop, "some-admin")
	if !errors.Is(err, ErrPrerequisiteIncomplete) {
		t.Fatalf("expected ErrPrerequisiteIncomplete, got: %v", err)
	}
	if repo.capturedTasks != nil {
		t.Fatal("must not complete the trip when pickup is not done")
	}
}

// TestAdminCompleteDropByOrder_FindsTripAndCompletes covers the order-scoped
// endpoint: given only an order id, it looks the trip up via GetByOrderID,
// resolves the assigned DE from the trip, and completes the drop.
func TestAdminCompleteDropByOrder_FindsTripAndCompletes(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:  "t3",
			OrderID: "ORD-ADMIN-3",
			DEID:    "de-1",
			DEPhone: "+260971000003",
			Status:  models.TripStatusOutForDelivery,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCompleted},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "5678"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000003"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "OUT_FOR_DELIVERY"}

	err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-ADMIN-3", "ops-user", "")
	if err != nil {
		t.Fatalf("expected order-scoped complete to succeed, got: %v", err)
	}

	var dropTask *models.Task
	for i := range repo.capturedTasks {
		if repo.capturedTasks[i].Type == models.TaskTypeDrop {
			dropTask = &repo.capturedTasks[i]
			break
		}
	}
	if dropTask == nil || dropTask.Status != models.TaskStatusCompleted {
		t.Fatalf("expected drop task completed, got: %+v", repo.capturedTasks)
	}
}

func TestAdminCompleteDropByOrder_NoTrip_JavaOnly(t *testing.T) {
	java := &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	svc := newTripServiceForTest(&stubTripRepo{}, &stubDERepo{}, &stubNotifier{})
	svc.javaClient = java
	if err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-J1", "ops", ""); err != nil {
		t.Fatal(err)
	}
	if len(java.updates) != 2 || java.updates[0] != "OUT_FOR_DELIVERY" || java.updates[1] != "DELIVERED" {
		t.Fatalf("updates=%v", java.updates)
	}
}

func TestAdminCompleteDropByOrder_ForceProgress_PickupThenDrop(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "t5", OrderID: "ORD-ADMIN-5", StoreID: "221",
		DEID: "de-1", DEPhone: "+260971000005",
		Status: models.TripStatusAccepted,
		Tasks: []models.Task{
			{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
			{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
		},
	}}
	de := &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000005", Status: models.DEStatusOffline}
	deRepo := &stubDERepo{de: de}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	if err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-ADMIN-5", "ops", ""); err != nil {
		t.Fatal(err)
	}
	if !repo.completeTripCalled {
		t.Fatal("expected drop to complete the trip")
	}
	if got := deRepo.statusCalls[len(deRepo.statusCalls)-1]; got != "+260971000005:offline" {
		t.Fatalf("expected restore offline, last call %q", got)
	}
}

func TestAdminCompleteDropByOrder_PickRider_RequiresPhone(t *testing.T) {
	svc := newTripServiceForTest(&stubTripRepo{trip: &models.Trip{
		TripID: "t1", OrderID: "ORD-U1", StoreID: "221", Status: models.TripStatusCreated,
	}}, &stubDERepo{}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-U1", "ops", "")
	if !errors.Is(err, ErrRiderRequired) {
		t.Fatalf("got %v", err)
	}
}

func TestAdminCompleteDropByOrder_PickRider_AssignsAndCompletes(t *testing.T) {
	de := &models.DeliveryExecutive{DEID: "de-9", PhoneNumber: "+260770990570", Status: models.DEStatusOffline, AssignedStoreID: "221"}
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "t1", OrderID: "ORD-U2", StoreID: "221", Status: models.TripStatusCreated,
		Tasks: []models.Task{
			{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
			{TaskID: "d", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
		},
	}}
	deRepo := &stubDERepo{de: de}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	if err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-U2", "ops", "+260770990570"); err != nil {
		t.Fatal(err)
	}
	if !repo.adminAssignCalled || !repo.completeTripCalled {
		t.Fatal("expected assign + complete")
	}
	if got := deRepo.statusCalls[len(deRepo.statusCalls)-1]; got != "+260770990570:offline" {
		t.Fatalf("restore offline, got %q", got)
	}
}

func TestAdminCompleteDropByOrder_BusyElsewhere(t *testing.T) {
	de := &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+2609", Status: models.DEStatusBusy, CurrentOrderID: "ORD-OTHER"}
	svc := newTripServiceForTest(&stubTripRepo{trip: &models.Trip{
		TripID: "t1", OrderID: "ORD-B1", DEPhone: "+2609", DEID: "de-1", Status: models.TripStatusAssigned,
	}}, &stubDERepo{de: de}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-B1", "ops", "")
	if !errors.Is(err, ErrRiderBusyElsewhere) {
		t.Fatalf("got %v", err)
	}
}

// Fix 1: on the force path the Java writes must be a single sequential walk
// (OUT_FOR_DELIVERY then DELIVERED) rather than two unordered goroutines, and a
// Java refusal must fail the POST. This test asserts the two writes land in
// order, synchronously, so no assertion needs to sleep for a goroutine.
func TestAdminCompleteDropByOrder_ForceProgress_JavaSequenced(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "t-seq", OrderID: "ORD-SEQ", StoreID: "221",
		DEID: "de-1", DEPhone: "+260971000009",
		Status: models.TripStatusAccepted,
		Tasks: []models.Task{
			{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
			{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
		},
	}}
	de := &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000009", Status: models.DEStatusOffline}
	java := &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	svc := newTripServiceForTest(repo, &stubDERepo{de: de}, &stubNotifier{})
	svc.javaClient = java
	if err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-SEQ", "ops", ""); err != nil {
		t.Fatal(err)
	}
	if len(java.updates) != 2 || java.updates[0] != "OUT_FOR_DELIVERY" || java.updates[1] != "DELIVERED" {
		t.Fatalf("expected sequential OFD then DELIVERED, got %v", java.updates)
	}
}

// Fix 1: a Java refusal on the force path must fail the POST (same guarantee as
// forceJavaDeliver on the no-trip path), not be swallowed by a fire-and-forget
// retry goroutine.
func TestAdminCompleteDropByOrder_ForceProgress_JavaRefusalFailsPOST(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "t-refuse", OrderID: "ORD-REFUSE", StoreID: "221",
		DEID: "de-1", DEPhone: "+260971000010",
		Status: models.TripStatusAccepted,
		Tasks: []models.Task{
			{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
			{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
		},
	}}
	de := &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000010", Status: models.DEStatusOffline}
	javaErr := errors.New("java refused transition")
	java := &stubJavaOrder{status: "READY_FOR_DELIVERY", updateErr: javaErr}
	svc := newTripServiceForTest(repo, &stubDERepo{de: de}, &stubNotifier{})
	svc.javaClient = java
	err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-REFUSE", "ops", "")
	if !errors.Is(err, javaErr) {
		t.Fatalf("expected the Java refusal to fail the POST, got %v", err)
	}
}

// Fix 3 (global constraint): Java already DELIVERED + an open trip must still
// close the trip, but must never POST Java DELIVERED again.
func TestAdminCompleteDropByOrder_ForceProgress_JavaDelivered_ClosesTripNoPost(t *testing.T) {
	repo := &stubTripRepo{trip: &models.Trip{
		TripID: "t-done", OrderID: "ORD-DONE", StoreID: "221",
		DEID: "de-1", DEPhone: "+260971000011",
		Status: models.TripStatusAccepted,
		Tasks: []models.Task{
			{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
			{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
		},
	}}
	de := &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000011", Status: models.DEStatusOffline}
	java := &stubJavaOrder{status: "DELIVERED"}
	svc := newTripServiceForTest(repo, &stubDERepo{de: de}, &stubNotifier{})
	svc.javaClient = java
	if err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-DONE", "ops", ""); err != nil {
		t.Fatal(err)
	}
	if !repo.completeTripCalled {
		t.Fatal("expected the trip to be closed even though Java was already DELIVERED")
	}
	if len(java.updates) != 0 {
		t.Fatalf("expected no Java writes when already DELIVERED, got %v", java.updates)
	}
}

// Fix 2: a force-assign that fails after the offline->eligible flip must restore
// the rider's prior status, otherwise an offline rider is stranded on duty and
// visible to the assignment cron.
func TestAdminCompleteDropByOrder_PickRider_AssignFails_RestoresPrior(t *testing.T) {
	de := &models.DeliveryExecutive{DEID: "de-9", PhoneNumber: "+260770990571", Status: models.DEStatusOffline, AssignedStoreID: "221"}
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID: "t-af", OrderID: "ORD-AF", StoreID: "221", Status: models.TripStatusCreated,
			Tasks: []models.Task{
				{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
				{TaskID: "d", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
			},
		},
		adminAssignErr: repository.ErrAdminAssignConflict,
	}
	deRepo := &stubDERepo{de: de}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-AF", "ops", "+260770990571")
	if err == nil {
		t.Fatal("expected an error when admin-assign fails")
	}
	if repo.completeTripCalled {
		t.Fatal("trip must not be completed when assign fails")
	}
	if got := deRepo.statusCalls[len(deRepo.statusCalls)-1]; got != "+260770990571:offline" {
		t.Fatalf("expected prior status restored to offline, last call %q (all: %v)", got, deRepo.statusCalls)
	}
	if de.Status != models.DEStatusOffline {
		t.Fatalf("expected rider left offline, got %s", de.Status)
	}
}

// Fix 2: an AdminAssign condition-failure must surface as the 409 sentinel
// ErrForceAssignConflict, not a bare repo error that classifies to 500.
func TestAdminCompleteDropByOrder_PickRider_AssignConflict_Sentinel(t *testing.T) {
	de := &models.DeliveryExecutive{DEID: "de-9", PhoneNumber: "+260770990572", Status: models.DEStatusOffline, AssignedStoreID: "221"}
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID: "t-cf", OrderID: "ORD-CF", StoreID: "221", Status: models.TripStatusAssigned,
			Tasks: []models.Task{
				{TaskID: "p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
				{TaskID: "d", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
			},
		},
		adminAssignErr: repository.ErrAdminAssignConflict,
	}
	svc := newTripServiceForTest(repo, &stubDERepo{de: de}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-CF", "ops", "+260770990572")
	if !errors.Is(err, ErrForceAssignConflict) {
		t.Fatalf("expected ErrForceAssignConflict, got %v", err)
	}
}

func TestAdminCompleteDropByOrder_TerminalTrip_Rejected(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:  "t4",
			OrderID: "ORD-ADMIN-4",
			DEID:    "de-1",
			DEPhone: "+260971000004",
			Status:  models.TripStatusCompleted,
			Tasks: []models.Task{
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCompleted},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000004"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "DELIVERED"}

	err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-ADMIN-4", "ops-user", "")
	if !errors.Is(err, ErrAlreadyDelivered) {
		t.Fatalf("expected ErrAlreadyDelivered, got: %v", err)
	}
}

// newCapturingJavaClient spins up a local httptest server standing in for the
// Java order-service and records the Actor-Id header of the last request it
// received, so tests can assert the actor string threaded all the way through
// syncJavaWithRetry without a real network dependency.
func newCapturingJavaClient(t *testing.T) (*JavaOrderClient, func() string) {
	t.Helper()
	var mu sync.Mutex
	var actor string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		actor = r.Header.Get("Actor-Id")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return NewJavaOrderClient(server.URL, logrus.New()), func() string {
		mu.Lock()
		defer mu.Unlock()
		return actor
	}
}

// waitForActor polls getActor for up to ~500ms since the Java sync runs on a
// goroutine (syncJavaWithRetry).
func waitForActor(getActor func() string) string {
	for i := 0; i < 50; i++ {
		if v := getActor(); v != "" {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ""
}

// TestAdminCompleteTask_Drop_JavaActorIsAdminUsername pins the actor-string
// contract end to end: admin-driven drop completion must sync Java with
// ADMIN:{username}, not DE:{deId}.
func TestAdminCompleteTask_Drop_JavaActorIsAdminUsername(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:  "t6",
			OrderID: "ORD-ADMIN-6",
			DEID:    "de-1",
			DEPhone: "+260971000006",
			Status:  models.TripStatusOutForDelivery,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCompleted},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{
		DEID: "de-1", PhoneNumber: "+260971000006", CurrentOrderID: "ORD-ADMIN-6",
	}}
	javaClient, getActor := newCapturingJavaClient(t)
	svc := &TripService{
		tripRepo:   repo,
		deRepo:     deRepo,
		notifier:   &stubNotifier{},
		javaClient: javaClient,
		logger:     logrus.New(),
	}

	err := svc.AdminCompleteTask(context.Background(), "+260971000006", models.TaskTypeDrop, "shivang")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := waitForActor(getActor)
	if got != "ADMIN:shivang" {
		t.Fatalf("Java actor = %q, want %q", got, "ADMIN:shivang")
	}
}

// TestUpdateTaskStatus_Drop_JavaActorIsRiderDEID is the rider-path mirror:
// driver-initiated completion (no admin username) must keep syncing Java as
// DE:{deId}.
func TestUpdateTaskStatus_Drop_JavaActorIsRiderDEID(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:  "t7",
			OrderID: "ORD-RIDER-1",
			DEID:    "de-2",
			Status:  models.TripStatusOutForDelivery,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCompleted},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-2", PhoneNumber: "+260971000007"}}
	javaClient, getActor := newCapturingJavaClient(t)
	svc := &TripService{
		tripRepo:   repo,
		deRepo:     deRepo,
		notifier:   &stubNotifier{},
		javaClient: javaClient,
		logger:     logrus.New(),
	}

	_, err := svc.UpdateTaskStatus(context.Background(), "t7", "task-drop", "+260971000007", models.TaskStatusCompleted, "1234", "", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := waitForActor(getActor)
	if got != "DE:de-2" {
		t.Fatalf("Java actor = %q, want %q", got, "DE:de-2")
	}
}

// Lusaka-ish drop coords. 0.001 deg lat ≈ 111m, so 0.002 ≈ 222m (outside 150m).
const (
	lusakaLat = -15.4167
	lusakaLng = 28.2833
)

type stubReachedConfig struct {
	cfg *models.TripReachedConfig
	err error
}

func (s stubReachedConfig) Get(context.Context) (*models.TripReachedConfig, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.cfg, nil
}

func floatPtr(v float64) *float64 { return &v }

func ofdDropTrip(dropLat, dropLng float64) *models.Trip {
	return &models.Trip{
		TripID:  "t-reached",
		OrderID: "ORD-REACHED",
		DEID:    "de-1",
		Status:  models.TripStatusOutForDelivery,
		Tasks: []models.Task{
			{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCompleted},
			{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234", Lat: dropLat, Lng: dropLng},
		},
	}
}

func reachedTestService(repo *stubTripRepo) *TripService {
	return newTripServiceForTest(repo, &stubDERepo{de: &models.DeliveryExecutive{
		DEID: "de-1", PhoneNumber: "+260971000001", CurrentOrderID: "ORD-REACHED",
	}}, &stubNotifier{})
}

func TestUpdateTaskStatus_DropReached_WithinRadius(t *testing.T) {
	repo := &stubTripRepo{trip: ofdDropTrip(lusakaLat, lusakaLng)}
	svc := reachedTestService(repo)

	result, err := svc.UpdateTaskStatus(context.Background(), "t-reached", "task-drop", "+260971000001", models.TaskStatusReached, "", "", floatPtr(lusakaLat), floatPtr(lusakaLng))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || result.Status != "updated" {
		t.Fatalf("result = %+v, want status updated", result)
	}
	if result.WithinRadius == nil || !*result.WithinRadius {
		t.Fatal("expected WithinRadius true")
	}
	if result.DistanceMeters == nil || *result.DistanceMeters > 150 {
		t.Fatalf("distance = %v, want <= 150", result.DistanceMeters)
	}
	if result.RadiusMeters == nil || *result.RadiusMeters != 150 {
		t.Fatalf("radius = %v, want 150", result.RadiusMeters)
	}

	drop := repo.trip.DropTask()
	if drop == nil || drop.Status != models.TaskStatusReached {
		t.Fatalf("drop status = %+v, want reached", drop)
	}
	if drop.ReachedAt == "" {
		t.Fatal("expected reached_at to be set")
	}
	if _, parseErr := time.Parse(time.RFC3339, drop.ReachedAt); parseErr != nil {
		t.Fatalf("reached_at %q is not RFC3339: %v", drop.ReachedAt, parseErr)
	}
	if !repo.updateTasksCalled {
		t.Fatal("expected UpdateTasks to persist reached")
	}
	if repo.completeTripCalled {
		t.Fatal("reached must not call CompleteTripAndFreeDE")
	}
}

func TestUpdateTaskStatus_DropReached_OutsideRadius(t *testing.T) {
	repo := &stubTripRepo{trip: ofdDropTrip(lusakaLat, lusakaLng)}
	svc := reachedTestService(repo)

	result, err := svc.UpdateTaskStatus(context.Background(), "t-reached", "task-drop", "+260971000001", models.TaskStatusReached, "", "", floatPtr(lusakaLat+0.002), floatPtr(lusakaLng))
	if err != nil {
		t.Fatalf("too-far must still succeed, got: %v", err)
	}
	if result.WithinRadius == nil || *result.WithinRadius {
		t.Fatal("expected WithinRadius false")
	}
	if result.DistanceMeters == nil || *result.DistanceMeters <= 150 {
		t.Fatalf("distance = %v, want > 150", result.DistanceMeters)
	}
	if result.RadiusMeters == nil || *result.RadiusMeters != 150 {
		t.Fatalf("radius = %v, want 150", result.RadiusMeters)
	}
	if repo.trip.DropTask().Status != models.TaskStatusReached {
		t.Fatal("too-far must still set status reached")
	}
	if repo.completeTripCalled {
		t.Fatal("reached must not complete the trip")
	}
}

func TestUpdateTaskStatus_DropReached_MissingLat(t *testing.T) {
	repo := &stubTripRepo{trip: ofdDropTrip(lusakaLat, lusakaLng)}
	svc := reachedTestService(repo)

	_, err := svc.UpdateTaskStatus(context.Background(), "t-reached", "task-drop", "+260971000001", models.TaskStatusReached, "", "", nil, floatPtr(lusakaLng))
	if !errors.Is(err, ErrMissingLocation) {
		t.Fatalf("got %v, want ErrMissingLocation", err)
	}
	if repo.trip.DropTask().Status != models.TaskStatusCreated {
		t.Fatalf("status = %q, want created", repo.trip.DropTask().Status)
	}
	if repo.updateTasksCalled {
		t.Fatal("missing lat must not write")
	}
}

func TestUpdateTaskStatus_DropReached_InvalidCoordinates(t *testing.T) {
	repo := &stubTripRepo{trip: ofdDropTrip(lusakaLat, lusakaLng)}
	svc := reachedTestService(repo)

	_, err := svc.UpdateTaskStatus(context.Background(), "t-reached", "task-drop", "+260971000001", models.TaskStatusReached, "", "", floatPtr(100), floatPtr(lusakaLng))
	if !errors.Is(err, ErrInvalidCoordinates) {
		t.Fatalf("out-of-range lat: got %v, want ErrInvalidCoordinates", err)
	}

	nan := math.NaN()
	_, err = svc.UpdateTaskStatus(context.Background(), "t-reached", "task-drop", "+260971000001", models.TaskStatusReached, "", "", &nan, floatPtr(lusakaLng))
	if !errors.Is(err, ErrInvalidCoordinates) {
		t.Fatalf("NaN lat: got %v, want ErrInvalidCoordinates", err)
	}
	if repo.updateTasksCalled {
		t.Fatal("invalid coords must not write")
	}
}

func TestUpdateTaskStatus_DropReached_CustomerZeroZero(t *testing.T) {
	repo := &stubTripRepo{trip: ofdDropTrip(0, 0)}
	svc := reachedTestService(repo)

	result, err := svc.UpdateTaskStatus(context.Background(), "t-reached", "task-drop", "+260971000001", models.TaskStatusReached, "", "", floatPtr(lusakaLat), floatPtr(lusakaLng))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.WithinRadius != nil || result.DistanceMeters != nil || result.RadiusMeters != nil {
		t.Fatalf("expected nil distance fields, got %+v", result)
	}
	drop := repo.trip.DropTask()
	if drop.Status != models.TaskStatusReached || drop.ReachedAt == "" {
		t.Fatalf("expected reached with reached_at, got %+v", drop)
	}
}

func TestUpdateTaskStatus_DropReached_Idempotent(t *testing.T) {
	repo := &stubTripRepo{trip: ofdDropTrip(lusakaLat, lusakaLng)}
	svc := reachedTestService(repo)

	if _, err := svc.UpdateTaskStatus(context.Background(), "t-reached", "task-drop", "+260971000001", models.TaskStatusReached, "", "", floatPtr(lusakaLat), floatPtr(lusakaLng)); err != nil {
		t.Fatalf("first reached: %v", err)
	}
	const frozen = "2026-01-01T00:00:00Z"
	repo.trip.DropTask().ReachedAt = frozen
	repo.updateTasksCalled = false

	result, err := svc.UpdateTaskStatus(context.Background(), "t-reached", "task-drop", "+260971000001", models.TaskStatusReached, "", "", floatPtr(lusakaLat), floatPtr(lusakaLng))
	if err != nil {
		t.Fatalf("second reached: %v", err)
	}
	if result.Status != "updated" {
		t.Fatalf("status = %q", result.Status)
	}
	if repo.trip.DropTask().ReachedAt != frozen {
		t.Fatalf("reached_at overwritten: %q", repo.trip.DropTask().ReachedAt)
	}
	if repo.updateTasksCalled {
		t.Fatal("idempotent reached must not write")
	}
}

func TestUpdateTaskStatus_PickupReached_NoWrite(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID: "t-pickup-reached",
			DEID:   "de-1",
			Status: models.TripStatusAccepted,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
			},
		},
	}
	svc := reachedTestService(repo)

	result, err := svc.UpdateTaskStatus(context.Background(), "t-pickup-reached", "task-pickup", "+260971000001", models.TaskStatusReached, "", "", floatPtr(lusakaLat), floatPtr(lusakaLng))
	if err != nil {
		t.Fatalf("pickup reached should no-op, got: %v", err)
	}
	if result == nil || result.Status != "updated" {
		t.Fatalf("result = %+v", result)
	}
	if repo.trip.PickupTask().Status != models.TaskStatusCreated {
		t.Fatalf("pickup status = %q, want created", repo.trip.PickupTask().Status)
	}
	if repo.updateTasksCalled || repo.completeTripCalled {
		t.Fatal("pickup reached must not write")
	}
}

func TestUpdateTaskStatus_DropReached_TripAccepted(t *testing.T) {
	trip := ofdDropTrip(lusakaLat, lusakaLng)
	trip.Status = models.TripStatusAccepted
	repo := &stubTripRepo{trip: trip}
	svc := reachedTestService(repo)

	_, err := svc.UpdateTaskStatus(context.Background(), "t-reached", "task-drop", "+260971000001", models.TaskStatusReached, "", "", floatPtr(lusakaLat), floatPtr(lusakaLng))
	if !errors.Is(err, ErrPrerequisiteIncomplete) {
		t.Fatalf("got %v, want ErrPrerequisiteIncomplete", err)
	}
	if repo.trip.DropTask().Status != models.TaskStatusCreated {
		t.Fatal("status must stay created when trip is not OFD")
	}
}

func TestUpdateTaskStatus_DropComplete_RequireReached_FromCreated(t *testing.T) {
	repo := &stubTripRepo{trip: ofdDropTrip(lusakaLat, lusakaLng)}
	svc := reachedTestService(repo)
	svc.reachedConfig = stubReachedConfig{cfg: &models.TripReachedConfig{RequireReachedBeforeComplete: true}}

	_, err := svc.UpdateTaskStatus(context.Background(), "t-reached", "task-drop", "+260971000001", models.TaskStatusCompleted, "1234", "", nil, nil)
	if !errors.Is(err, ErrDropNotReached) {
		t.Fatalf("got %v, want ErrDropNotReached", err)
	}
	if repo.completeTripCalled {
		t.Fatal("must not complete from created when flag is on")
	}
}

func TestUpdateTaskStatus_DropComplete_FromReached(t *testing.T) {
	trip := ofdDropTrip(lusakaLat, lusakaLng)
	trip.DropTask().Status = models.TaskStatusReached
	trip.DropTask().ReachedAt = "2026-08-22T10:00:00Z"
	repo := &stubTripRepo{trip: trip}
	svc := reachedTestService(repo)
	svc.reachedConfig = stubReachedConfig{cfg: &models.TripReachedConfig{RequireReachedBeforeComplete: true}}

	result, err := svc.UpdateTaskStatus(context.Background(), "t-reached", "task-drop", "+260971000001", models.TaskStatusCompleted, "1234", "", nil, nil)
	if err != nil {
		t.Fatalf("complete from reached: %v", err)
	}
	if result == nil || result.Status != "updated" {
		t.Fatalf("result = %+v", result)
	}
	if !repo.completeTripCalled {
		t.Fatal("expected CompleteTripAndFreeDE")
	}
	var drop *models.Task
	for i := range repo.capturedTasks {
		if repo.capturedTasks[i].Type == models.TaskTypeDrop {
			drop = &repo.capturedTasks[i]
			break
		}
	}
	if drop == nil || drop.Status != models.TaskStatusCompleted {
		t.Fatalf("expected drop completed, got %+v", repo.capturedTasks)
	}
}

func TestUpdateTaskStatus_DropComplete_ConfigGetError_FailsOpen(t *testing.T) {
	repo := &stubTripRepo{trip: ofdDropTrip(lusakaLat, lusakaLng)}
	svc := reachedTestService(repo)
	svc.reachedConfig = stubReachedConfig{err: errors.New("ddb down")}

	result, err := svc.UpdateTaskStatus(context.Background(), "t-reached", "task-drop", "+260971000001", models.TaskStatusCompleted, "1234", "", nil, nil)
	if err != nil {
		t.Fatalf("config Get error must fail open, got: %v", err)
	}
	if result == nil || result.Status != "updated" {
		t.Fatalf("result = %+v, want status updated", result)
	}
	if !repo.completeTripCalled {
		t.Fatal("expected drop complete to persist")
	}
}

func TestUpdateTaskStatus_PickupComplete_ConfigGetError_Succeeds(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:  "t-pickup-cfg",
			OrderID: "ORD-PICKUP-CFG",
			DEID:    "de-1",
			Status:  models.TripStatusAccepted,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
			},
		},
	}
	svc := newTripServiceForTest(repo, &stubDERepo{de: &models.DeliveryExecutive{
		DEID: "de-1", PhoneNumber: "+260971000001",
	}}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	svc.reachedConfig = stubReachedConfig{err: errors.New("ddb down")}

	result, err := svc.UpdateTaskStatus(context.Background(), "t-pickup-cfg", "task-pickup", "+260971000001", models.TaskStatusCompleted, "", "", nil, nil)
	if err != nil {
		t.Fatalf("pickup complete must not read/fail on reached config, got: %v", err)
	}
	if result == nil || result.Status != "updated" {
		t.Fatalf("result = %+v, want status updated", result)
	}
}

func TestAdminCompleteDropByOrder_Drop_SynthesizesReached(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:  "t-admin-order-reached",
			OrderID: "ORD-ADMIN-ORDER-REACHED",
			DEID:    "de-1",
			DEPhone: "+260971000001",
			Status:  models.TripStatusOutForDelivery,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCompleted},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "9999"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{
		DEID: "de-1", PhoneNumber: "+260971000001",
	}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "OUT_FOR_DELIVERY"}

	if err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-ADMIN-ORDER-REACHED", "ops", ""); err != nil {
		t.Fatalf("admin drop by order: %v", err)
	}

	var drop *models.Task
	for i := range repo.capturedTasks {
		if repo.capturedTasks[i].Type == models.TaskTypeDrop {
			drop = &repo.capturedTasks[i]
			break
		}
	}
	if drop == nil || drop.Status != models.TaskStatusCompleted {
		t.Fatalf("expected completed drop, got %+v", repo.capturedTasks)
	}
	if drop.ReachedAt == "" {
		t.Fatal("admin through-reached must set reached_at")
	}
	if _, parseErr := time.Parse(time.RFC3339, drop.ReachedAt); parseErr != nil {
		t.Fatalf("reached_at %q is not RFC3339: %v", drop.ReachedAt, parseErr)
	}
}

func TestAdminCompleteTask_Drop_SynthesizesReached(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:  "t-admin-reached",
			OrderID: "ORD-ADMIN-REACHED",
			DEID:    "de-1",
			DEPhone: "+260971000001",
			Status:  models.TripStatusOutForDelivery,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCompleted},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "9999"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{
		DEID: "de-1", PhoneNumber: "+260971000001", CurrentOrderID: "ORD-ADMIN-REACHED",
	}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})

	if err := svc.AdminCompleteTask(context.Background(), "+260971000001", models.TaskTypeDrop, "ops"); err != nil {
		t.Fatalf("admin drop: %v", err)
	}

	var drop *models.Task
	for i := range repo.capturedTasks {
		if repo.capturedTasks[i].Type == models.TaskTypeDrop {
			drop = &repo.capturedTasks[i]
			break
		}
	}
	if drop == nil || drop.Status != models.TaskStatusCompleted {
		t.Fatalf("expected completed drop, got %+v", repo.capturedTasks)
	}
	if drop.ReachedAt == "" {
		t.Fatal("admin through-reached must set reached_at")
	}
	if _, parseErr := time.Parse(time.RFC3339, drop.ReachedAt); parseErr != nil {
		t.Fatalf("reached_at %q is not RFC3339: %v", drop.ReachedAt, parseErr)
	}
}

func TestAdminCompleteTask_Drop_KeepsExistingReachedAt(t *testing.T) {
	const frozen = "2026-08-01T12:00:00Z"
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:  "t-admin-keep",
			OrderID: "ORD-ADMIN-KEEP",
			DEID:    "de-1",
			DEPhone: "+260971000001",
			Status:  models.TripStatusOutForDelivery,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCompleted},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusReached, ReachedAt: frozen, OTP: "1234"},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{
		DEID: "de-1", PhoneNumber: "+260971000001", CurrentOrderID: "ORD-ADMIN-KEEP",
	}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})

	if err := svc.AdminCompleteTask(context.Background(), "+260971000001", models.TaskTypeDrop, "ops"); err != nil {
		t.Fatalf("admin drop: %v", err)
	}

	var drop *models.Task
	for i := range repo.capturedTasks {
		if repo.capturedTasks[i].Type == models.TaskTypeDrop {
			drop = &repo.capturedTasks[i]
			break
		}
	}
	if drop == nil || drop.Status != models.TaskStatusCompleted {
		t.Fatalf("expected completed drop, got %+v", repo.capturedTasks)
	}
	if drop.ReachedAt != frozen {
		t.Fatalf("reached_at = %q, want %q", drop.ReachedAt, frozen)
	}
}

func TestPreviewAdminDropByOrder_NoTrip_JavaReady(t *testing.T) {
	svc := newTripServiceForTest(&stubTripRepo{}, &stubDERepo{}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	p, err := svc.PreviewAdminDropByOrder(context.Background(), "ORD-P1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != AdminDropModeJavaOnly {
		t.Fatalf("mode=%s", p.Mode)
	}
}

func TestPreviewAdminDropByOrder_NoTrip_JavaPacking_Blocked(t *testing.T) {
	svc := newTripServiceForTest(&stubTripRepo{}, &stubDERepo{}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "PACKING"}
	p, err := svc.PreviewAdminDropByOrder(context.Background(), "ORD-P2")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != AdminDropModeBlocked || p.Reason != "java_not_ready" {
		t.Fatalf("got %+v", p)
	}
}

func TestPreviewAdminDropByOrder_JavaCancelled_Blocked(t *testing.T) {
	svc := newTripServiceForTest(&stubTripRepo{trip: &models.Trip{TripID: "t1", Status: models.TripStatusCreated}}, &stubDERepo{}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "CANCELLED"}
	p, err := svc.PreviewAdminDropByOrder(context.Background(), "ORD-P3")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != AdminDropModeBlocked || p.Reason != "java_cancelled" {
		t.Fatalf("got %+v", p)
	}
}

func TestPreviewAdminDropByOrder_UnassignedTrip_PickRider(t *testing.T) {
	listed := []*models.DeliveryExecutive{
		{PhoneNumber: "+2601", Name: "Ann", Status: models.DEStatusOffline, InHandCashZMW: 10, AssignedStoreID: "221"},
		{PhoneNumber: "+2602", Name: "Bob", Status: models.DEStatusBusy, InHandCashZMW: 1, AssignedStoreID: "221"},
		{PhoneNumber: "+2603", Name: "Cyd", Status: models.DEStatusFree, InHandCashZMW: 99, AssignedStoreID: "221"},
	}
	svc := newTripServiceForTest(&stubTripRepo{trip: &models.Trip{
		TripID: "t1", OrderID: "ORD-P4", StoreID: "221", Status: models.TripStatusCreated,
	}}, &stubDERepo{listed: listed}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	p, err := svc.PreviewAdminDropByOrder(context.Background(), "ORD-P4")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != AdminDropModePickRider {
		t.Fatalf("mode=%s", p.Mode)
	}
	if len(p.Candidates) != 2 {
		t.Fatalf("candidates=%d (busy must be excluded)", len(p.Candidates))
	}
}

func TestPreviewAdminDropByOrder_AssignedRider_ForceProgress(t *testing.T) {
	de := &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+2609", Name: "Ghan", Status: models.DEStatusOffline}
	svc := newTripServiceForTest(&stubTripRepo{trip: &models.Trip{
		TripID: "t1", OrderID: "ORD-P5", DEPhone: "+2609", DEID: "de-1", Status: models.TripStatusAccepted,
	}}, &stubDERepo{de: de}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	p, err := svc.PreviewAdminDropByOrder(context.Background(), "ORD-P5")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != AdminDropModeForceProgress || p.Rider == nil || p.Rider.Phone != "+2609" {
		t.Fatalf("got %+v", p)
	}
}

func TestPreviewAdminDropByOrder_RiderBusyElsewhere_Blocked(t *testing.T) {
	de := &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+2609", Status: models.DEStatusBusy, CurrentOrderID: "ORD-OTHER"}
	svc := newTripServiceForTest(&stubTripRepo{trip: &models.Trip{
		TripID: "t1", OrderID: "ORD-P6", DEPhone: "+2609", DEID: "de-1", Status: models.TripStatusAssigned,
	}}, &stubDERepo{de: de}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "READY_FOR_DELIVERY"}
	p, err := svc.PreviewAdminDropByOrder(context.Background(), "ORD-P6")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != AdminDropModeBlocked || p.Reason != "rider_busy_elsewhere" {
		t.Fatalf("got %+v", p)
	}
}

func TestPreviewAdminDropByOrder_AlreadyDone(t *testing.T) {
	svc := newTripServiceForTest(&stubTripRepo{trip: &models.Trip{
		TripID: "t1", OrderID: "ORD-P7", Status: models.TripStatusCompleted,
	}}, &stubDERepo{}, &stubNotifier{})
	svc.javaClient = &stubJavaOrder{status: "DELIVERED"}
	p, err := svc.PreviewAdminDropByOrder(context.Background(), "ORD-P7")
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != AdminDropModeAlreadyDone {
		t.Fatalf("mode=%s", p.Mode)
	}
}
