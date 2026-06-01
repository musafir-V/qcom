package service

import (
	"context"
	"fmt"
	"time"

	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
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
	if err != nil || de == nil {
		return nil, op.Fail(fmt.Errorf("DE not found"))
	}
	if de.CurrentOrderID == "" {
		return nil, nil
	}

	trip, err := s.tripRepo.GetByOrderID(ctx, de.CurrentOrderID)
	if err != nil {
		return nil, op.Fail(err)
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
		return op.Outcome("not_found", fmt.Errorf("trip %s not found", tripID))
	}

	// 2. Verify caller owns this trip
	de, err := s.deRepo.GetByPhone(ctx, callerDEPhone)
	if err != nil || de == nil {
		return op.Fail(fmt.Errorf("DE not found"))
	}
	if trip.DEID != de.DEID {
		return op.Outcome("forbidden", fmt.Errorf("trip is not assigned to this DE"))
	}

	// 3. Trip-level guard: reject if already closed
	if trip.Status == models.TripStatusCompleted || trip.Status == models.TripStatusCancelled {
		return op.Outcome("trip_closed", fmt.Errorf("trip is already %s", trip.Status))
	}

	// 4. Find task
	task := trip.TaskByID(taskID)
	if task == nil {
		return op.Outcome("not_found", fmt.Errorf("task %s not found on trip", taskID))
	}

	// 5. Cross-task ordering: drop cannot advance until pickup is completed
	if task.Type == models.TaskTypeDrop {
		if err := validateCrossTaskOrdering(trip, task); err != nil {
			return op.Outcome("prerequisite_incomplete", err)
		}
	}

	// 6. Validate the specific transition
	if err := validateTaskTransition(*task, newStatus, otp); err != nil {
		return op.Outcome("invalid_transition", err)
	}

	// 7. Apply transition
	task.Status = newStatus
	if err := s.tripRepo.UpdateTasks(ctx, tripID, trip.Tasks); err != nil {
		return op.Fail(err)
	}

	// 8. Mirror trip status and trigger Java sync
	s.onTaskCompleted(ctx, trip, task, de)

	return nil
}

// onTaskCompleted updates trip status and asynchronously syncs Java when needed.
func (s *TripService) onTaskCompleted(ctx context.Context, trip *models.Trip, task *models.Task, de *models.DeliveryExecutive) {
	switch {
	case task.Type == models.TaskTypePickup && task.Status == models.TaskStatusCompleted:
		_ = s.tripRepo.UpdateStatus(ctx, trip.TripID, models.TripStatusInTransit)
		// Async: notify Java OUT_FOR_DELIVERY
		go s.syncJavaWithRetry(trip.OrderID, "OUT_FOR_DELIVERY", de.DEID)

	case task.Type == models.TaskTypeDrop && task.Status == models.TaskStatusReached:
		_ = s.tripRepo.UpdateStatus(ctx, trip.TripID, models.TripStatusReached)

	case task.Type == models.TaskTypeDrop && task.Status == models.TaskStatusCompleted:
		_ = s.tripRepo.UpdateStatus(ctx, trip.TripID, models.TripStatusCompleted)
		// Async: notify Java DELIVERED
		go s.syncJavaWithRetry(trip.OrderID, "DELIVERED", de.DEID)
		// Increment daily count and free the DE
		go s.completeDelivery(trip, de)
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

// completeDelivery increments the DE's daily count, updates TotalTripsCompleted,
// and transitions DE status to free. Runs in a goroutine.
func (s *TripService) completeDelivery(trip *models.Trip, de *models.DeliveryExecutive) {
	ctx := context.Background()

	// Payout computation + ledger write (increments the DE daily count internally).
	if s.payoutService != nil {
		s.payoutService.OnTripCompleted(ctx, trip, de.PhoneNumber)
	}

	if err := s.deRepo.UpdateStatus(ctx, de.PhoneNumber, models.DEStatusFree, "", ""); err != nil {
		s.logger.WithError(err).WithField("de_phone", de.PhoneNumber).
			Error("failed to set DE free after trip completion")
	}
}

// validateTaskTransition checks that the requested status transition is valid
// for the given task type and current status.
func validateTaskTransition(task models.Task, newStatus models.TaskStatus, otp string) error {
	if task.Status == newStatus {
		return fmt.Errorf("task is already in state %q", newStatus)
	}

	switch task.Type {
	case models.TaskTypePickup:
		// Only valid API transition: arrived → completed
		if task.Status == models.TaskStatusArrived && newStatus == models.TaskStatusCompleted {
			return nil
		}
		return fmt.Errorf("invalid pickup transition: %s → %s (only arrived→completed is allowed via API)", task.Status, newStatus)

	case models.TaskTypeDrop:
		switch {
		case task.Status == models.TaskStatusCreated && newStatus == models.TaskStatusReached:
			// Requires correct OTP
			if task.OTP != otp {
				return fmt.Errorf("invalid OTP")
			}
			return nil
		case task.Status == models.TaskStatusReached && newStatus == models.TaskStatusCompleted:
			return nil
		default:
			return fmt.Errorf("invalid drop transition: %s → %s", task.Status, newStatus)
		}
	}

	return fmt.Errorf("unknown task type: %s", task.Type)
}

// validateCrossTaskOrdering enforces that the drop task cannot advance
// until the pickup task is completed.
func validateCrossTaskOrdering(trip *models.Trip, task *models.Task) error {
	if task.Type != models.TaskTypeDrop {
		return nil
	}
	pickup := trip.PickupTask()
	if pickup == nil {
		return fmt.Errorf("trip has no pickup task")
	}
	if pickup.Status != models.TaskStatusCompleted {
		return fmt.Errorf("pickup task must be completed before drop task can advance (pickup status: %s)", pickup.Status)
	}
	return nil
}
