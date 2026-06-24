package service

import (
	"context"
	"errors"
	"testing"

	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

func TestValidateTaskTransition_CreatedToCompleted(t *testing.T) {
	for _, taskType := range []models.TaskType{models.TaskTypePickup, models.TaskTypeDrop} {
		task := models.Task{Type: taskType, Status: models.TaskStatusCreated}
		if err := validateTaskTransition(task, models.TaskStatusCompleted); err != nil {
			t.Fatalf("%s: expected valid transition, got: %v", taskType, err)
		}
	}
}

func TestValidateTaskTransition_LegacyStatusToCompleted(t *testing.T) {
	// Tasks already in legacy intermediate states can still be completed.
	for _, status := range []models.TaskStatus{"arrived", "reached"} {
		task := models.Task{Type: models.TaskTypeDrop, Status: status}
		if err := validateTaskTransition(task, models.TaskStatusCompleted); err != nil {
			t.Fatalf("status %q: expected valid transition, got: %v", status, err)
		}
	}
}

func TestValidateTaskTransition_NonCompletedTarget_Rejected(t *testing.T) {
	task := models.Task{Type: models.TaskTypePickup, Status: models.TaskStatusCreated}
	if err := validateTaskTransition(task, models.TaskStatus("arrived")); err == nil {
		t.Fatal("expected error: only completed is allowed")
	}
}

func TestValidateTaskTransition_AlreadyCompleted_Rejected(t *testing.T) {
	task := models.Task{Type: models.TaskTypePickup, Status: models.TaskStatusCompleted}
	if err := validateTaskTransition(task, models.TaskStatusCompleted); err == nil {
		t.Fatal("expected error: re-entering completed state")
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

// --- test helpers ---

// stubTripRepo satisfies tripRepoI. GetByOrderID and CancelByOrderID are used by
// CancelTripByOrder. GetByID and CompleteTripAndFreeDE are used by UpdateTaskStatus
// (drop path). updateTasksFn, if set, is called by UpdateTasks (pickup path).
type stubTripRepo struct {
	trip           *models.Trip
	cancelCalled   bool
	cancelTripID   string
	cancelDEPhone  string
	updateTasksFn  func(ctx context.Context, tripID string, tasks []models.Task) error
	capturedTasks  []models.Task
}

func (s *stubTripRepo) GetByOrderID(_ context.Context, _ string) (*models.Trip, error) {
	return s.trip, nil
}

func (s *stubTripRepo) CancelByOrderID(_ context.Context, tripID, dePhone string) error {
	s.cancelCalled = true
	s.cancelTripID = tripID
	s.cancelDEPhone = dePhone
	return nil
}

func (s *stubTripRepo) GetByID(_ context.Context, _ string) (*models.Trip, error) {
	return s.trip, nil
}

func (s *stubTripRepo) CompleteTripAndFreeDE(_ context.Context, _, _ string, tasks []models.Task, _ float64) error {
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

func (s *stubTripRepo) UpdateStatus(_ context.Context, _ string, _ models.TripStatus) error {
	panic("stubTripRepo.UpdateStatus: unexpected call")
}

func (s *stubTripRepo) Accept(_ context.Context, _, _ string) error {
	panic("stubTripRepo.Accept: unexpected call")
}

func (s *stubTripRepo) RejectToPool(_ context.Context, _, _, _, _ string) error {
	panic("stubTripRepo.RejectToPool: unexpected call")
}

// stubDERepo satisfies deRepoI for unit tests.
type stubDERepo struct {
	de *models.DeliveryExecutive
}

func (s *stubDERepo) GetByPhone(_ context.Context, _ string) (*models.DeliveryExecutive, error) {
	return s.de, nil
}

// stubNotifier satisfies NotificationService. Only Send is used by CancelTripByOrder.
type stubNotifier struct {
	sent    bool
	lastReq models.NotificationSendRequest
}

func (s *stubNotifier) Send(_ context.Context, req models.NotificationSendRequest) models.NotificationSendResult {
	s.sent = true
	s.lastReq = req
	return models.NotificationSendResult{Status: models.SendStatusSent}
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
// repos, suitable for testing UpdateTaskStatus paths.
func newTripServiceForTest(repo *stubTripRepo, deRepo *stubDERepo) *TripService {
	return &TripService{
		tripRepo: repo,
		deRepo:   deRepo,
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
	svc := newTripServiceForTest(repo, deRepo)

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
