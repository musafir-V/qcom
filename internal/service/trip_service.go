package service

import (
	"context"
	"errors"
	"fmt"
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
		if err := s.tripRepo.UpdateStatus(bgCtx, trip.TripID, models.TripStatusInTransit); err != nil {
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
