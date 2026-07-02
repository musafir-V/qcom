package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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
	CompleteTripAndFreeDE(ctx context.Context, tripID, dePhone, storeID string, tasks []models.Task, codAmount float64) error
	UpdateTasks(ctx context.Context, tripID string, tasks []models.Task) error
	UpdateStatus(ctx context.Context, tripID string, status models.TripStatus) error
	Accept(ctx context.Context, tripID, deID string) error
	RejectToPool(ctx context.Context, tripID, dePhone, storeID, deID string) error
	CancelByOrderID(ctx context.Context, tripID, dePhone, storeID string) error
	UpdatePayment(ctx context.Context, tripID string, payment *models.Payment) error
}

// deRepoI is the subset of DERepository methods used by TripService.
// Narrowing to an interface allows unit tests to inject stubs without DynamoDB.
type deRepoI interface {
	GetByPhone(ctx context.Context, phone string) (*models.DeliveryExecutive, error)
}

// statusEventAppender appends DE status-event log entries (presence timeline).
type statusEventAppender interface {
	Append(ctx context.Context, event *models.DEStatusEvent) error
}

type TripService struct {
	tripRepo        tripRepoI
	deRepo          deRepoI
	javaClient      *JavaOrderClient
	payoutService   *PayoutService
	notifier        NotificationService
	statusEventRepo statusEventAppender
	logger          *logrus.Logger
}

func NewTripService(
	tripRepo *repository.TripRepository,
	deRepo *repository.DERepository,
	javaClient *JavaOrderClient,
	payoutService *PayoutService,
	notifier NotificationService,
	statusEventRepo *repository.DEStatusEventRepository,
	logger *logrus.Logger,
) *TripService {
	return &TripService{
		tripRepo:        tripRepo,
		deRepo:          deRepo,
		javaClient:      javaClient,
		payoutService:   payoutService,
		notifier:        notifier,
		statusEventRepo: statusEventRepo,
		logger:          logger,
	}
}

// appendStatusEvent writes a status-event best-effort; never fails the caller.
func (s *TripService) appendStatusEvent(ctx context.Context, event *models.DEStatusEvent) {
	if s.statusEventRepo == nil {
		return
	}
	if err := s.statusEventRepo.Append(ctx, event); err != nil {
		s.logger.WithError(err).WithField("phone", event.Phone).
			Warn("failed to append DE status event")
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

	if err := s.tripRepo.CancelByOrderID(ctx, trip.TripID, trip.DEPhone, trip.StoreID); err != nil {
		return fmt.Errorf("CancelTripByOrder: db cancel failed: %w", err)
	}

	if trip.DEPhone != "" {
		s.appendStatusEvent(ctx, &models.DEStatusEvent{
			Phone:     trip.DEPhone,
			FromState: models.DEStatusBusy,
			ToState:   models.DEStatusFree,
			Reason:    models.ReasonCancelled,
			StoreID:   trip.StoreID,
			TS:        timezone.Now().UTC().Format(time.RFC3339),
		})
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

// PaymentUpdateInput is an upstream (order-service) payment change for an order.
type PaymentUpdateInput struct {
	OrderID       string
	PaymentMethod string
	GrandTotal    float64
	Currency      string
}

// PaymentUpdateResult describes the outcome of UpdateTripPayment so the HTTP
// handler can pick the right status code.
type PaymentUpdateResult struct {
	Updated bool
	// Reason is empty when Updated is true; otherwise "no_active_trip" (no trip
	// exists yet for the order) or "trip_terminal" (trip already closed).
	Reason string
}

// UpdateTripPayment re-snapshots a trip's payment after the order's payment
// method changed upstream (e.g. a COD order was paid online). It is idempotent
// and safe to call regardless of trip existence/state:
//   - no trip yet  -> no-op (the trip will be created from the updated order)
//   - active trip  -> overwrite payment, push the rider to re-sync
//   - terminal trip-> rejected (cash may already be collected/accrued)
//
// payment is derived from the same mapping used at trip creation, so creation
// and update always produce identical snapshots.
func (s *TripService) UpdateTripPayment(ctx context.Context, in PaymentUpdateInput) (PaymentUpdateResult, error) {
	op := logging.Start(ctx, s.logger, "TripService.UpdateTripPayment", logrus.Fields{
		"order_id": in.OrderID, "payment_method": in.PaymentMethod,
	})
	defer op.End()

	trip, err := s.tripRepo.GetByOrderID(ctx, in.OrderID)
	if err != nil {
		return PaymentUpdateResult{}, op.Fail(err)
	}
	if trip == nil {
		op.Outcome("no_active_trip", nil)
		return PaymentUpdateResult{Updated: false, Reason: "no_active_trip"}, nil
	}

	payment := paymentFromOrder(JavaOrder{
		PaymentMethod: in.PaymentMethod,
		GrandTotal:    in.GrandTotal,
		Currency:      in.Currency,
	})

	if err := s.tripRepo.UpdatePayment(ctx, trip.TripID, payment); err != nil {
		if errors.Is(err, repository.ErrTripTerminal) {
			op.Outcome("trip_terminal", nil)
			return PaymentUpdateResult{Updated: false, Reason: "trip_terminal"}, nil
		}
		return PaymentUpdateResult{}, op.Fail(err)
	}

	op.With("collect_cash", payment.CollectCash)
	s.notifyDriverPaymentUpdated(ctx, trip, payment)
	return PaymentUpdateResult{Updated: true}, nil
}

// notifyDriverPaymentUpdated sends the assigned rider a quiet heads-up so their
// app re-polls and they don't (e.g.) ask for cash that's already been paid.
//
// It only fires when the rider is actively working the trip (accepted /
// out_for_delivery) AND the cash requirement materially changed (collect_cash
// flipped, or it's still COD but the amount changed). For created/assigned trips
// the rider hasn't engaged yet, so the 10s polling backstop is sufficient — this
// also avoids the loud order-assignment channel used for high-priority driver
// pushes (this one is Normal priority → quiet default channel).
func (s *TripService) notifyDriverPaymentUpdated(ctx context.Context, trip *models.Trip, payment *models.Payment) {
	if s.notifier == nil || trip.DEID == "" {
		return
	}
	if trip.Status != models.TripStatusAccepted && trip.Status != models.TripStatusOutForDelivery {
		return
	}

	oldCollect := trip.Payment != nil && trip.Payment.CollectCash
	oldAmount := 0.0
	if trip.Payment != nil {
		oldAmount = trip.Payment.AmountZMW
	}
	cashChanged := oldCollect != payment.CollectCash ||
		(payment.CollectCash && oldAmount != payment.AmountZMW)
	if !cashChanged {
		return
	}

	shortOrder := trip.OrderID
	if len(shortOrder) > 12 {
		shortOrder = shortOrder[:12]
	}
	body := "Customer paid online for order " + shortOrder + " — no cash to collect."
	if payment.CollectCash {
		body = "Order " + shortOrder + " is now cash on delivery — collect K" +
			strconv.FormatFloat(payment.AmountZMW, 'f', 2, 64) + "."
	}

	s.notifier.Send(ctx, models.NotificationSendRequest{
		RecipientType: models.RecipientTypeDriver,
		RecipientID:   trip.DEID,
		EventType:     "PAYMENT_UPDATED",
		Priority:      models.PriorityNormal,
		Title:         "Payment updated",
		Body:          body,
		Data: map[string]string{
			"type":         "PAYMENT_UPDATED",
			"trip_id":      trip.TripID,
			"order_id":     trip.OrderID,
			"collect_cash": strconv.FormatBool(payment.CollectCash),
		},
	})
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
// photoS3Key is optional; when non-empty it is stored on the task record before persistence.
func (s *TripService) UpdateTaskStatus(ctx context.Context, tripID, taskID, callerDEPhone string, newStatus models.TaskStatus, otp, photoS3Key string) error {
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

	if photoS3Key != "" {
		task.PhotoS3Key = photoS3Key
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
		if err := s.tripRepo.CompleteTripAndFreeDE(ctx, trip.TripID, de.PhoneNumber, trip.StoreID, trip.Tasks, codAccrualAmount(trip)); err != nil {
			return err
		}
		s.appendStatusEvent(ctx, &models.DEStatusEvent{
			Phone:     de.PhoneNumber,
			FromState: models.DEStatusBusy,
			ToState:   models.DEStatusFree,
			Reason:    models.ReasonDelivered,
			StoreID:   trip.StoreID,
			TS:        timezone.Now().UTC().Format(time.RFC3339),
		})
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
	if s.javaClient == nil {
		return
	}
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
	op := logging.Start(ctx, s.logger, "TripService.GetTripForPhotoPresign", logrus.Fields{
		"trip_id": tripID, "task_id": taskID,
	})
	defer op.End()

	trip, _, err := s.ownedTrip(ctx, op, tripID, callerPhone)
	if err != nil {
		return nil, nil, err
	}
	task := trip.TaskByID(taskID)
	if task == nil {
		return nil, nil, op.Outcome("not_found", fmt.Errorf("%w: %s", ErrTaskNotFound, taskID))
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
