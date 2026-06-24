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
	"github.com/qcom/qcom/internal/timezone"
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

// tripRepoI is the subset of TripRepository methods used by TripService.
// Using an interface here allows unit tests to inject stub implementations
// without spinning up a real DynamoDB client.
type tripRepoI interface {
	GetByOrderID(ctx context.Context, orderID string) (*models.Trip, error)
	GetByID(ctx context.Context, tripID string) (*models.Trip, error)
	CompleteTripAndFreeDE(ctx context.Context, tripID, dePhone string, tasks []models.Task, codAmount float64) error
	UpdateTasks(ctx context.Context, tripID string, tasks []models.Task) error
	UpdateStatus(ctx context.Context, tripID string, status models.TripStatus) error
	Accept(ctx context.Context, tripID, deID string) error
	RejectToPool(ctx context.Context, tripID, dePhone, storeID, deID string) error
	CancelByOrderID(ctx context.Context, tripID, dePhone string) error
}

type TripService struct {
	tripRepo      tripRepoI
	deRepo        *repository.DERepository
	javaClient    *JavaOrderClient
	payoutService *PayoutService
	notifier      NotificationService
	logger        *logrus.Logger
}

func NewTripService(
	tripRepo *repository.TripRepository,
	deRepo *repository.DERepository,
	javaClient *JavaOrderClient,
	payoutService *PayoutService,
	notifier NotificationService,
	logger *logrus.Logger,
) *TripService {
	return &TripService{
		tripRepo:      tripRepo,
		deRepo:        deRepo,
		javaClient:    javaClient,
		payoutService: payoutService,
		notifier:      notifier,
		logger:        logger,
	}
}

// CancelTripByOrder is called by the order-service when an order is cancelled (any actor).
// It looks up the trip, guards against terminal statuses, cancels it atomically via DynamoDB,
// and fires a rider push notification if a DE is assigned. PN errors are fire-and-forget;
// all other errors are returned to the caller so the HTTP handler can decide on fail-open policy.
func (s *TripService) CancelTripByOrder(ctx context.Context, orderID, reason string) error {
	trip, err := s.tripRepo.GetByOrderID(ctx, orderID)
	if err != nil {
		return fmt.Errorf("CancelTripByOrder: lookup failed: %w", err)
	}
	if trip == nil {
		s.logger.WithField("order_id", orderID).Debug("CancelTripByOrder: no trip found — no-op")
		return nil
	}
	if trip.Status == models.TripStatusCompleted {
		s.logger.WithFields(logrus.Fields{"order_id": orderID, "trip_id": trip.TripID}).
			Warn("CancelTripByOrder: trip already completed — skipping cancel")
		return nil
	}
	if trip.Status == models.TripStatusCancelled {
		s.logger.WithFields(logrus.Fields{"order_id": orderID, "trip_id": trip.TripID}).
			Debug("CancelTripByOrder: trip already cancelled — idempotent no-op")
		return nil
	}

	if err := s.tripRepo.CancelByOrderID(ctx, trip.TripID, trip.DEPhone); err != nil {
		return fmt.Errorf("CancelTripByOrder: db cancel failed: %w", err)
	}

	if trip.DEID != "" {
		shortOrder := orderID
		if len(shortOrder) > 12 {
			shortOrder = shortOrder[:12]
		}
		s.notifier.Send(ctx, models.NotificationSendRequest{
			RecipientType: models.RecipientTypeDriver,
			RecipientID:   trip.DEID,
			EventType:     "TRIP_CANCELLED",
			Priority:      models.PriorityHigh,
			Title:         "Trip cancelled",
			Body:          "Your delivery for order " + shortOrder + " has been cancelled.",
			Data: map[string]string{
				"type":     "TRIP_CANCELLED",
				"trip_id":  trip.TripID,
				"order_id": orderID,
			},
		})
	}
	return nil
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

	return s.applyTaskCompletion(ctx, trip, task, de, newStatus, otp)
}

// AdminCompleteTask marks pickup or drop done for a driver's current trip.
// Pickup skips bill QR verification; drop still requires the customer OTP.
func (s *TripService) AdminCompleteTask(ctx context.Context, driverPhone string, taskType models.TaskType, otp string) error {
	op := logging.Start(ctx, s.logger, "TripService.AdminCompleteTask", logrus.Fields{
		"phone": driverPhone, "task_type": string(taskType),
	})
	defer op.End()

	de, err := s.deRepo.GetByPhone(ctx, driverPhone)
	if err != nil {
		return op.Fail(err)
	}
	if de == nil {
		return op.Outcome("not_found", fmt.Errorf("DE not found"))
	}

	trip, err := s.GetCurrentTrip(ctx, driverPhone)
	if err != nil {
		return op.Fail(err)
	}
	if trip == nil {
		return op.Outcome("not_found", fmt.Errorf("%w: no active trip", ErrTripNotFound))
	}

	var task *models.Task
	switch taskType {
	case models.TaskTypePickup:
		task = trip.PickupTask()
	case models.TaskTypeDrop:
		task = trip.DropTask()
	default:
		return op.Outcome("not_found", fmt.Errorf("%w: unsupported task type", ErrTaskNotFound))
	}
	if task == nil {
		return op.Outcome("not_found", fmt.Errorf("%w: %s task missing", ErrTaskNotFound, taskType))
	}

	if err := validateTaskTransition(*task, models.TaskStatusCompleted); err != nil {
		return op.Outcome("invalid_transition", err)
	}
	if err := validateTaskAgainstTripStatus(task.Type, trip.Status); err != nil {
		return op.Outcome("prerequisite_incomplete", err)
	}
	if task.Type == models.TaskTypeDrop {
		if err := validateDropOTP(*task, otp); err != nil {
			return op.Outcome("invalid_otp", err)
		}
	}

	return s.applyTaskCompletion(ctx, trip, task, de, models.TaskStatusCompleted, otp)
}

func (s *TripService) applyTaskCompletion(ctx context.Context, trip *models.Trip, task *models.Task, de *models.DeliveryExecutive, newStatus models.TaskStatus, otp string) error {
	task.Status = newStatus
	if (task.Type == models.TaskTypePickup || task.Type == models.TaskTypeDrop) && newStatus == models.TaskStatusCompleted {
		task.CompletedAt = timezone.Now().Format(time.RFC3339)
	}

	if task.Type == models.TaskTypeDrop && newStatus == models.TaskStatusCompleted {
		if err := s.tripRepo.CompleteTripAndFreeDE(ctx, trip.TripID, de.PhoneNumber, trip.Tasks, codAccrualAmount(trip)); err != nil {
			return err
		}
		go s.syncJavaWithRetry(trip.OrderID, "DELIVERED", de.DEID)
		s.notifyCustomerOrderDelivered(trip.OrderID)
		s.recordTripPayout(trip, de)
		return nil
	}

	if err := s.tripRepo.UpdateTasks(ctx, trip.TripID, trip.Tasks); err != nil {
		return err
	}

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
		s.notifyCustomerOutForDelivery(trip.OrderID)

	}
}

func (s *TripService) notifyCustomerOutForDelivery(orderID string) {
	s.notifyCustomer(orderID, "ORDER_OUT_FOR_DELIVERY", models.PriorityHigh,
		"On the way!", "Your order is out for delivery.")
}

func (s *TripService) notifyCustomerOrderDelivered(orderID string) {
	s.notifyCustomer(orderID, "ORDER_DELIVERED", models.PriorityHigh,
		"Delivered!", "Your order has been delivered.")
}

func (s *TripService) notifyCustomer(orderID, eventType string, priority models.NotificationPriority, title, body string) {
	if s.notifier == nil {
		return
	}
	go func() {
		ctx := context.Background()
		target, err := s.javaClient.GetNotificationTarget(ctx, orderID)
		if err != nil {
			s.logger.WithError(err).WithField("order_id", orderID).Warn("customer notification target lookup failed")
			return
		}
		if target == nil || target.CustomerID == "" {
			s.logger.WithField("order_id", orderID).Debug("customer notification skipped — no target")
			return
		}

		orderRef := target.OrderNumber
		if orderRef == "" {
			orderRef = orderID
		}
		data := map[string]string{
			"order_id": orderRef,
		}
		if target.OrderUUID != "" {
			data["order_uuid"] = target.OrderUUID
		}

		s.notifier.Send(ctx, models.NotificationSendRequest{
			RecipientType: models.RecipientTypeCustomer,
			RecipientID:   target.CustomerID,
			EventType:     eventType,
			Priority:      priority,
			Title:         title,
			Body:          body,
			Data:          data,
		})
	}()
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

// codAccrualAmount returns the cash a DE collects on completing this trip's
// drop. Only COD (collect_cash) trips accrue in-hand cash; prepaid trips are 0.
func codAccrualAmount(trip *models.Trip) float64 {
	if trip.Payment != nil && trip.Payment.CollectCash {
		return trip.Payment.AmountZMW
	}
	return 0
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

// GetTripForPhotoPresign fetches a trip and validates the caller is the assigned DE.
// Returns the trip and specified task. Used by the photo presign endpoint.
func (s *TripService) GetTripForPhotoPresign(ctx context.Context, tripID, taskID, callerPhone string) (*models.Trip, *models.Task, error) {
	trip, err := s.tripRepo.GetByID(ctx, tripID)
	if err != nil {
		return nil, nil, err
	}
	if trip == nil {
		return nil, nil, fmt.Errorf("%w: %s", ErrTripNotFound, tripID)
	}
	de, err := s.deRepo.GetByPhone(ctx, callerPhone)
	if err != nil {
		return nil, nil, err
	}
	if de == nil || trip.DEID != de.DEID {
		return nil, nil, ErrTripForbidden
	}
	task := trip.TaskByID(taskID)
	if task == nil {
		return nil, nil, fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}
	return trip, task, nil
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
