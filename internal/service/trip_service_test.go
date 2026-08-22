package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
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
	// Tasks already in legacy intermediate states can still be completed.
	for _, status := range []models.TaskStatus{"arrived", "reached"} {
		task := models.Task{Type: models.TaskTypeDrop, Status: status}
		if err := validateTaskTransition(task, models.TaskStatusCompleted, false); err != nil {
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

// --- test helpers ---

// stubTripRepo satisfies tripRepoI. GetByOrderID and CancelByOrderID are used by
// CancelTripByOrder. GetByID and CompleteTripAndFreeDE are used by UpdateTaskStatus
// (drop path). updateTasksFn, if set, is called by UpdateTasks (pickup path).
type stubTripRepo struct {
	trip                *models.Trip
	cancelCalled        bool
	cancelTripID        string
	cancelDEPhone       string
	updateTasksFn       func(ctx context.Context, tripID string, tasks []models.Task) error
	capturedTasks       []models.Task
	updatePaymentCalled bool
	updatePaymentErr    error
	capturedPayment     *models.Payment
	updateStatusCalled  bool
	updateStatusTripID  string
	updateStatusStatus  models.TripStatus
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
	s.capturedTasks = make([]models.Task, len(tasks))
	copy(s.capturedTasks, tasks)
	return nil
}

func (s *stubTripRepo) UpdateTasks(ctx context.Context, tripID string, tasks []models.Task) error {
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

func (s *stubTripRepo) Accept(_ context.Context, _, _ string) error {
	panic("stubTripRepo.Accept: unexpected call")
}

func (s *stubTripRepo) RejectToPool(_ context.Context, _, _, _, _ string) error {
	panic("stubTripRepo.RejectToPool: unexpected call")
}

func (s *stubTripRepo) UpdatePayment(_ context.Context, _ string, payment *models.Payment) error {
	s.updatePaymentCalled = true
	s.capturedPayment = payment
	return s.updatePaymentErr
}

// stubDERepo satisfies deRepoI for unit tests.
type stubDERepo struct {
	de *models.DeliveryExecutive
}

func (s *stubDERepo) GetByPhone(_ context.Context, _ string) (*models.DeliveryExecutive, error) {
	return s.de, nil
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

	err := svc.UpdateTaskStatus(context.Background(), "t1", "task-drop", "+260971000001", models.TaskStatusCompleted, "1234", "orders/ORD-001/drop/de-1/abc.jpg")
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

	err := svc.UpdateTaskStatus(context.Background(), "t1", "task-drop", "+260971000001", models.TaskStatusCompleted, "1234", "")
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

	err := svc.UpdateTaskStatus(context.Background(), "t2", "task-pickup", "+260971000001", models.TaskStatusCompleted, "", "")
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

	err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-ADMIN-3", "ops-user")
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

func TestAdminCompleteDropByOrder_NoTrip_NotFound(t *testing.T) {
	repo := &stubTripRepo{trip: nil}
	deRepo := &stubDERepo{}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})

	err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-MISSING", "ops-user")
	if !errors.Is(err, ErrTripNotFound) {
		t.Fatalf("expected ErrTripNotFound, got: %v", err)
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

	err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-ADMIN-4", "ops-user")
	if !errors.Is(err, ErrTripClosed) {
		t.Fatalf("expected ErrTripClosed, got: %v", err)
	}
}

func TestAdminCompleteDropByOrder_RequiresOutForDelivery(t *testing.T) {
	repo := &stubTripRepo{
		trip: &models.Trip{
			TripID:  "t5",
			OrderID: "ORD-ADMIN-5",
			DEID:    "de-1",
			DEPhone: "+260971000005",
			Status:  models.TripStatusAccepted,
			Tasks: []models.Task{
				{TaskID: "task-pickup", Type: models.TaskTypePickup, Status: models.TaskStatusCreated},
				{TaskID: "task-drop", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
			},
		},
	}
	deRepo := &stubDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971000005"}}
	svc := newTripServiceForTest(repo, deRepo, &stubNotifier{})

	err := svc.AdminCompleteDropByOrder(context.Background(), "ORD-ADMIN-5", "ops-user")
	if !errors.Is(err, ErrPrerequisiteIncomplete) {
		t.Fatalf("expected ErrPrerequisiteIncomplete, got: %v", err)
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

	err := svc.UpdateTaskStatus(context.Background(), "t7", "task-drop", "+260971000007", models.TaskStatusCompleted, "1234", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := waitForActor(getActor)
	if got != "DE:de-2" {
		t.Fatalf("Java actor = %q, want %q", got, "DE:de-2")
	}
}
