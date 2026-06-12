package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

// Sentinel errors returned by UpdateTaskStatus and the task transition
// validators. Callers should classify these with errors.Is rather than
// matching on error text. Wrapping with %w preserves the sentinel identity
// while allowing a human-readable detail to be appended.
var (
	ErrTripNotFound           = errors.New("trip not found")
	ErrTaskNotFound           = errors.New("task not found")
	ErrTripForbidden          = errors.New("trip not assigned to this DE")
	ErrTripClosed             = errors.New("trip already closed")
	ErrPrerequisiteIncomplete = errors.New("prerequisite task incomplete")
	ErrInvalidTransition      = errors.New("invalid task transition")
	ErrInvalidOTP             = errors.New("invalid OTP")
	ErrInvalidTripTransition  = errors.New("invalid trip transition")
	ErrPickupOrderMismatch    = errors.New("scanned order does not match assigned trip")
)

type TripService struct {
	tripRepo      *repository.TripRepository
	deRepo        *repository.DERepository
	javaClient    *JavaOrderClient
	payoutService *PayoutService
	logger        *logrus.Logger
}

func NewTripService(
	tripRepo *repository.TripRepository,
	deRepo *repository.DERepository,
	javaClient *JavaOrderClient,
	payoutService *PayoutService,
	logger *logrus.Logger,
) *TripService {
	return &TripService{
		tripRepo:      tripRepo,
		deRepo:        deRepo,
		javaClient:    javaClient,
		payoutService: payoutService,
		logger:        logger,
	}
}

// GetCurrentTrip returns the active trip for the calling DE.
func (s *TripService) GetCurrentTrip(ctx context.Context, dePhone string) (*models.Trip, error) {
	op := logging.Start(ctx, s.logger, "TripService.GetCurrentTrip", logrus.Fields{"phone": dePhone})
	defer op.End()

	de, err := s.deRepo.GetByPhone(ctx, dePhone)
	if err != nil {
		return nil, op.Fail(err)
	}
	if de == nil {
		return nil, op.Outcome("not_found", fmt.Errorf("DE not found"))
	}
	if de.CurrentOrderID == "" {
		return nil, nil
	}

	trip, err := s.tripRepo.GetByOrderID(ctx, de.CurrentOrderID)
	if err != nil {
		return nil, op.Fail(err)
	}
	// Guard against stale current_order_id pointing at a closed trip.
	if trip != nil && (trip.Status == models.TripStatusCompleted || trip.Status == models.TripStatusCancelled) {
		return nil, nil
	}
	return trip, nil
}

// UpdateTaskStatus validates and applies a task status transition.
// callerDEPhone is extracted from the JWT and used to verify trip ownership.
func (s *TripService) UpdateTaskStatus(ctx context.Context, tripID, taskID, callerDEPhone string, newStatus models.TaskStatus, otp string) error {
	op := logging.Start(ctx, s.logger, "TripService.UpdateTaskStatus", logrus.Fields{
		"trip_id": tripID, "task_id": taskID, "new_status": string(newStatus),
	})
	defer op.End()

	// 1. Fetch trip
	trip, err := s.tripRepo.GetByID(ctx, tripID)
	if err != nil {
		return op.Fail(err)
	}
	if trip == nil {
		return op.Outcome("not_found", fmt.Errorf("%w: %s", ErrTripNotFound, tripID))
	}

	// 2. Verify caller owns this trip
	de, err := s.deRepo.GetByPhone(ctx, callerDEPhone)
	if err != nil {
		return op.Fail(err)
	}
	if de == nil {
		return op.Outcome("forbidden", ErrTripForbidden)
	}
	if trip.DEID != de.DEID {
		return op.Outcome("forbidden", ErrTripForbidden)
	}

	// 3. Trip-level guard: reject if already closed
	if trip.Status == models.TripStatusCompleted || trip.Status == models.TripStatusCancelled {
		return op.Outcome("trip_closed", fmt.Errorf("%w: %s", ErrTripClosed, trip.Status))
	}

	// 4. Find task
	task := trip.TaskByID(taskID)
	if task == nil {
		return op.Outcome("not_found", fmt.Errorf("%w: %s", ErrTaskNotFound, taskID))
	}

	// 5. Validate the specific transition
	if err := validateTaskTransition(*task, newStatus); err != nil {
		return op.Outcome("invalid_transition", err)
	}

	// Trip-status gate: pickup requires accepted, drop requires out_for_delivery.
	if err := validateTaskAgainstTripStatus(task.Type, trip.Status); err != nil {
		return op.Outcome("prerequisite_incomplete", err)
	}

	// Drop completion requires the customer OTP.
	if task.Type == models.TaskTypeDrop && newStatus == models.TaskStatusCompleted {
		if err := validateDropOTP(*task, otp); err != nil {
			return op.Outcome("invalid_otp", err)
		}
	}

	// 6. Apply transition
	task.Status = newStatus

	if task.Type == models.TaskTypeDrop && newStatus == models.TaskStatusCompleted {
		if err := s.tripRepo.CompleteTripAndFreeDE(ctx, tripID, de.PhoneNumber, trip.Tasks); err != nil {
			return op.Fail(err)
		}
		go s.syncJavaWithRetry(trip.OrderID, "DELIVERED", de.DEID)
		go s.recordTripPayout(trip, de)
		return nil
	}

	if err := s.tripRepo.UpdateTasks(ctx, tripID, trip.Tasks); err != nil {
		return op.Fail(err)
	}

	// 7. Mirror trip status and trigger Java sync (pickup completion)
	s.onTaskCompleted(ctx, trip, task, de)

	return nil
}

// onTaskCompleted updates trip status and asynchronously syncs Java when needed.
func (s *TripService) onTaskCompleted(ctx context.Context, trip *models.Trip, task *models.Task, de *models.DeliveryExecutive) {
	// Use a detached context for all writes — the request context may be cancelled
	// if the client disconnects before these writes complete.
	bgCtx := context.Background()

	switch {
	case task.Type == models.TaskTypePickup && task.Status == models.TaskStatusCompleted:
		if err := s.tripRepo.UpdateStatus(bgCtx, trip.TripID, models.TripStatusOutForDelivery); err != nil {
			s.logger.WithError(err).WithField("trip_id", trip.TripID).Error("failed to mirror trip status")
		}
		// Async: notify Java OUT_FOR_DELIVERY
		go s.syncJavaWithRetry(trip.OrderID, "OUT_FOR_DELIVERY", de.DEID)

	}
}

// syncJavaWithRetry retries the Java status update up to 3 times with backoff.
// Runs in a goroutine — does not block the DE response.
func (s *TripService) syncJavaWithRetry(orderID, status, deID string) {
	ctx := context.Background()
	backoff := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}
	for i, delay := range backoff {
		if err := s.javaClient.UpdateOrderStatus(ctx, orderID, status, "DE:"+deID); err != nil {
			s.logger.WithError(err).WithFields(logrus.Fields{
				"order_id": orderID, "status": status, "attempt": i + 1,
			}).Warn("java sync retry")
			time.Sleep(delay)
			continue
		}
		return
	}
	s.logger.WithFields(logrus.Fields{
		"order_id": orderID, "status": status,
	}).Error("java sync failed after 3 attempts — cron compensation will retry")
}

// recordTripPayout writes payout ledger entries after trip completion.
// DE status is already set to free atomically with the trip completion transaction.
func (s *TripService) recordTripPayout(trip *models.Trip, de *models.DeliveryExecutive) {
	if s.payoutService == nil {
		return
	}
	s.payoutService.OnTripCompleted(context.Background(), trip, de.PhoneNumber)
}

// validateTaskTransition allows any non-completed task to move directly to completed.
func validateTaskTransition(task models.Task, newStatus models.TaskStatus) error {
	if newStatus != models.TaskStatusCompleted {
		return fmt.Errorf("%w: only transition to completed is allowed", ErrInvalidTransition)
	}
	if task.Status == models.TaskStatusCompleted {
		return fmt.Errorf("%w: task is already completed", ErrInvalidTransition)
	}
	return nil
}

// validateDropOTP checks the OTP provided by the DE against the drop task OTP.
func validateDropOTP(task models.Task, otp string) error {
	if strings.TrimSpace(otp) == "" {
		return fmt.Errorf("%w: OTP is required", ErrInvalidOTP)
	}
	if task.OTP != otp {
		return fmt.Errorf("%w", ErrInvalidOTP)
	}
	return nil
}

// validateTaskAgainstTripStatus enforces that a task may only be completed when
// the trip is in the correct status: pickup requires the trip to be accepted;
// drop requires the trip to be out_for_delivery (i.e. pickup already done).
func validateTaskAgainstTripStatus(taskType models.TaskType, status models.TripStatus) error {
	switch taskType {
	case models.TaskTypePickup:
		if status != models.TripStatusAccepted {
			return fmt.Errorf("%w: accept the trip before starting pickup (status=%s)", ErrPrerequisiteIncomplete, status)
		}
	case models.TaskTypeDrop:
		if status != models.TripStatusOutForDelivery {
			return fmt.Errorf("%w: complete pickup before drop (status=%s)", ErrPrerequisiteIncomplete, status)
		}
	}
	return nil
}

// AcceptTrip moves an assigned trip to accepted for the calling DE.
func (s *TripService) AcceptTrip(ctx context.Context, tripID, callerDEPhone string) error {
	op := logging.Start(ctx, s.logger, "TripService.AcceptTrip", logrus.Fields{"trip_id": tripID})
	defer op.End()

	trip, de, err := s.ownedTrip(ctx, op, tripID, callerDEPhone)
	if err != nil {
		return err
	}
	if trip.Status != models.TripStatusAssigned {
		return op.Outcome("invalid_state", fmt.Errorf("%w: cannot accept from %s", ErrInvalidTripTransition, trip.Status))
	}
	if err := s.tripRepo.Accept(ctx, tripID, de.DEID); err != nil {
		return op.Fail(err)
	}
	return nil
}

// RejectTrip returns an assigned trip to the pool and the DE to eligible.
// Only legal while the trip is still assigned — once accepted it cannot be rejected.
func (s *TripService) RejectTrip(ctx context.Context, tripID, callerDEPhone string) error {
	op := logging.Start(ctx, s.logger, "TripService.RejectTrip", logrus.Fields{"trip_id": tripID})
	defer op.End()

	trip, de, err := s.ownedTrip(ctx, op, tripID, callerDEPhone)
	if err != nil {
		return err
	}
	if trip.Status != models.TripStatusAssigned {
		return op.Outcome("invalid_state", fmt.Errorf("%w: cannot reject from %s", ErrInvalidTripTransition, trip.Status))
	}
	if err := s.tripRepo.RejectToPool(ctx, tripID, de.PhoneNumber, trip.StoreID, de.DEID); err != nil {
		return op.Fail(err)
	}
	return nil
}

// validatePickupScan checks a scanned bill QR (which encodes the order_id)
// against the trip. The trip must be accepted (pickup not yet done) and the
// scanned order_id must match the trip's order.
func validatePickupScan(trip *models.Trip, scannedOrderID string) error {
	if trip.Status != models.TripStatusAccepted {
		return fmt.Errorf("%w: cannot verify pickup from %s", ErrInvalidTripTransition, trip.Status)
	}
	if scannedOrderID == "" || scannedOrderID != trip.OrderID {
		return ErrPickupOrderMismatch
	}
	return nil
}

// VerifyPickup confirms the DE scanned the correct bill QR for their trip.
// It does not mutate state — the swipe-to-confirm afterward completes the
// pickup task via UpdateTaskStatus.
func (s *TripService) VerifyPickup(ctx context.Context, tripID, callerDEPhone, scannedOrderID string) error {
	op := logging.Start(ctx, s.logger, "TripService.VerifyPickup", logrus.Fields{"trip_id": tripID})
	defer op.End()

	trip, _, err := s.ownedTrip(ctx, op, tripID, callerDEPhone)
	if err != nil {
		return err
	}
	if err := validatePickupScan(trip, scannedOrderID); err != nil {
		return op.Outcome("verify_failed", err)
	}
	return nil
}

// ownedTrip fetches a trip and verifies the caller (by phone) owns it.
func (s *TripService) ownedTrip(ctx context.Context, op *logging.Op, tripID, callerDEPhone string) (*models.Trip, *models.DeliveryExecutive, error) {
	trip, err := s.tripRepo.GetByID(ctx, tripID)
	if err != nil {
		return nil, nil, op.Fail(err)
	}
	if trip == nil {
		return nil, nil, op.Outcome("not_found", fmt.Errorf("%w: %s", ErrTripNotFound, tripID))
	}
	de, err := s.deRepo.GetByPhone(ctx, callerDEPhone)
	if err != nil {
		return nil, nil, op.Fail(err)
	}
	if de == nil || trip.DEID != de.DEID {
		return nil, nil, op.Outcome("forbidden", ErrTripForbidden)
	}
	return trip, de, nil
}
