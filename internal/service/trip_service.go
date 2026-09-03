package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/money"
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
	ErrDropNotReached         = errors.New("drop not reached")
	ErrMissingLocation        = errors.New("missing location")
	ErrInvalidCoordinates     = errors.New("invalid coordinates")
	ErrInvalidOTP             = errors.New("invalid OTP")
	ErrInvalidTripTransition  = errors.New("invalid trip transition")
	ErrPickupOrderMismatch    = errors.New("scanned order does not match assigned trip")
	ErrOrderNotDeliverable    = errors.New("order not deliverable")
	ErrOrderNotPacked         = errors.New("order is not packed")
	ErrJavaOrderCancelled     = errors.New("java order cancelled")
	ErrRiderRequired          = errors.New("rider required")
	ErrRiderBusyElsewhere     = errors.New("rider busy on another trip")
	ErrAlreadyDelivered       = errors.New("already delivered")
	ErrAlreadyOutForDelivery  = errors.New("already out for delivery")
	ErrForceAssignConflict    = errors.New("force-assign conflict: refresh and retry")
)

// TaskUpdateResult is returned by UpdateTaskStatus. Status is always "updated"
// on success. Distance fields are set only for drop-reached when customer
// coordinates are usable.
type TaskUpdateResult struct {
	Status         string   `json:"status"`
	WithinRadius   *bool    `json:"within_radius,omitempty"`
	DistanceMeters *float64 `json:"distance_meters,omitempty"`
	RadiusMeters   *float64 `json:"radius_meters,omitempty"`
}

// reachedConfigStore loads the drop-reached geofence / compat flag.
// A nil store on TripService applies the same defaults as a missing row.
type reachedConfigStore interface {
	Get(ctx context.Context) (*models.TripReachedConfig, error)
}

// dropDeadlineConfigStore loads the driver drop-task countdown x/y config.
// A nil store on TripService applies the same defaults as a missing row.
type dropDeadlineConfigStore interface {
	Get(ctx context.Context) (*models.DropDeadlineConfig, error)
}

// tripRepoI is the subset of TripRepository methods used by TripService.
// Using an interface here allows unit tests to inject stub implementations
// without spinning up a real DynamoDB client.
type tripRepoI interface {
	GetByOrderID(ctx context.Context, orderID string) (*models.Trip, error)
	GetByID(ctx context.Context, tripID string) (*models.Trip, error)
	CompleteTripAndFreeDE(ctx context.Context, tripID, dePhone, storeID string, tasks []models.Task, codAmount float64) error
	CompleteTripOnly(ctx context.Context, tripID string, tasks []models.Task) error
	UpdateTasks(ctx context.Context, tripID string, tasks []models.Task) error
	UpdateStatus(ctx context.Context, tripID string, status models.TripStatus) error
	MarkOutForDelivery(ctx context.Context, tripID string, dropDeadline int64) error
	Accept(ctx context.Context, tripID, deID string) error
	RejectToPool(ctx context.Context, tripID, dePhone, storeID, deID string) error
	CancelByOrderID(ctx context.Context, tripID, dePhone, storeID string) error
	UpdatePayment(ctx context.Context, tripID string, payment *models.Payment) error
	UpdateEditByOrder(ctx context.Context, tripID string, items []models.TripItem, payment *models.Payment, tasks []models.Task) error
	AdminAssign(ctx context.Context, tripID, orderID, deID, dePhone, storeID string) error
	MarkAdminOFDInbound(ctx context.Context, tripID string) error
}

// deRepoI is the subset of DERepository methods used by TripService.
// Narrowing to an interface allows unit tests to inject stubs without DynamoDB.
type deRepoI interface {
	GetByPhone(ctx context.Context, phone string) (*models.DeliveryExecutive, error)
	UpdateStatus(ctx context.Context, phone string, status models.DEStatus, storeID, orderID string) error
	AttachToTrip(ctx context.Context, phone, orderID, tripID, storeID string) error
	ListByAssignedStore(ctx context.Context, indexKey, namePrefix, cursor string, limit int32) ([]*models.DeliveryExecutive, string, error)
}

// javaOrderAPI is the subset of JavaOrderClient used by TripService so tests
// can inject a stub. *JavaOrderClient already satisfies it.
type javaOrderAPI interface {
	GetOrderStatus(ctx context.Context, orderID string) (string, error)
	UpdateOrderStatus(ctx context.Context, orderID, status, actorID string) error
}

// statusEventAppender appends DE status-event log entries (presence timeline).
type statusEventAppender interface {
	Append(ctx context.Context, event *models.DEStatusEvent) error
}

type TripService struct {
	tripRepo           tripRepoI
	deRepo             deRepoI
	javaClient         javaOrderAPI
	payoutService      *PayoutService
	notifier           NotificationService
	statusEventRepo    statusEventAppender
	reachedConfig      reachedConfigStore
	dropDeadlineConfig dropDeadlineConfigStore
	logger             *logrus.Logger
}

func NewTripService(
	tripRepo *repository.TripRepository,
	deRepo *repository.DERepository,
	javaClient *JavaOrderClient,
	payoutService *PayoutService,
	notifier NotificationService,
	statusEventRepo *repository.DEStatusEventRepository,
	reachedConfig reachedConfigStore,
	dropDeadlineConfig dropDeadlineConfigStore,
	logger *logrus.Logger,
) *TripService {
	return &TripService{
		tripRepo:           tripRepo,
		deRepo:             deRepo,
		javaClient:         javaClient,
		payoutService:      payoutService,
		notifier:           notifier,
		statusEventRepo:    statusEventRepo,
		reachedConfig:      reachedConfig,
		dropDeadlineConfig: dropDeadlineConfig,
		logger:             logger,
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

// EditTripByOrderInput is an upstream (order-service) packed-snapshot edit for an order.
type EditTripByOrderInput struct {
	OrderID       string
	PaymentMethod string
	GrandTotal    float64
	Currency      string
	DeliveryZone  string
	Items         []EditTripItemInput
}

// EditTripItemInput is one packed line item from the edit-by-order body.
type EditTripItemInput struct {
	SKU      string
	Name     string
	ImageURL string
	Quantity int
}

// EditTripByOrder overwrites a trip's packed snapshot (items, payment, pickup
// delivery zone) after the order is re-packed upstream. Idempotent; no rider push.
func (s *TripService) EditTripByOrder(ctx context.Context, in EditTripByOrderInput) (PaymentUpdateResult, error) {
	op := logging.Start(ctx, s.logger, "TripService.EditTripByOrder", logrus.Fields{
		"order_id": in.OrderID,
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

	items := make([]models.TripItem, 0, len(in.Items))
	for _, it := range in.Items {
		items = append(items, models.TripItem{
			Sku:      it.SKU,
			Name:     it.Name,
			ImageURL: it.ImageURL,
			Quantity: it.Quantity,
		})
	}

	payment := paymentFromOrder(JavaOrder{
		PaymentMethod: in.PaymentMethod,
		GrandTotal:    in.GrandTotal,
		Currency:      in.Currency,
	})

	tasks := append([]models.Task(nil), trip.Tasks...)
	for i := range tasks {
		if tasks[i].Type == models.TaskTypePickup {
			tasks[i].DeliveryZone = in.DeliveryZone
		}
	}

	if err := s.tripRepo.UpdateEditByOrder(ctx, trip.TripID, items, payment, tasks); err != nil {
		if errors.Is(err, repository.ErrTripTerminal) {
			op.Outcome("trip_terminal", nil)
			return PaymentUpdateResult{Updated: false, Reason: "trip_terminal"}, nil
		}
		return PaymentUpdateResult{}, op.Fail(err)
	}

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
// lat/lng are required for drop reached; ignored for pickup reached and for complete.
func (s *TripService) UpdateTaskStatus(ctx context.Context, tripID, taskID, callerDEPhone string, newStatus models.TaskStatus, otp, photoS3Key string, lat, lng *float64) (*TaskUpdateResult, error) {
	op := logging.Start(ctx, s.logger, "TripService.UpdateTaskStatus", logrus.Fields{
		"trip_id": tripID, "task_id": taskID, "new_status": string(newStatus),
	})
	defer op.End()

	// 1. Fetch trip
	trip, err := s.tripRepo.GetByID(ctx, tripID)
	if err != nil {
		return nil, op.Fail(err)
	}
	if trip == nil {
		return nil, op.Outcome("not_found", fmt.Errorf("%w: %s", ErrTripNotFound, tripID))
	}

	// 2. Verify caller owns this trip
	de, err := s.deRepo.GetByPhone(ctx, callerDEPhone)
	if err != nil {
		return nil, op.Fail(err)
	}
	if de == nil {
		return nil, op.Outcome("forbidden", ErrTripForbidden)
	}
	if trip.DEID != de.DEID {
		return nil, op.Outcome("forbidden", ErrTripForbidden)
	}

	// 3. Trip-level guard: reject if already closed
	if trip.Status == models.TripStatusCompleted || trip.Status == models.TripStatusCancelled {
		return nil, op.Outcome("trip_closed", fmt.Errorf("%w: %s", ErrTripClosed, trip.Status))
	}

	// 4. Find task
	task := trip.TaskByID(taskID)
	if task == nil {
		return nil, op.Outcome("not_found", fmt.Errorf("%w: %s", ErrTaskNotFound, taskID))
	}

	// Pickup reached is a no-op: no writes, lat/lng ignored.
	if task.Type == models.TaskTypePickup && newStatus == models.TaskStatusReached {
		return &TaskUpdateResult{Status: "updated"}, nil
	}

	if newStatus == models.TaskStatusReached {
		if err := validateTaskAgainstTripStatus(task.Type, trip.Status); err != nil {
			return nil, op.Outcome("prerequisite_incomplete", err)
		}
		if task.Status == models.TaskStatusCompleted {
			return nil, op.Outcome("invalid_transition", fmt.Errorf("%w: task is already completed", ErrInvalidTransition))
		}
		if task.Status != models.TaskStatusReached {
			if err := validateTaskTransition(*task, newStatus, false); err != nil {
				return nil, op.Outcome("invalid_transition", err)
			}
		}
		if err := validateDriverCoords(lat, lng); err != nil {
			outcome := "missing_location"
			if errors.Is(err, ErrInvalidCoordinates) {
				outcome = "invalid_coordinates"
			}
			return nil, op.Outcome(outcome, err)
		}
		result, err := s.applyDropReached(ctx, trip, task, *lat, *lng)
		if err != nil {
			return nil, op.Fail(err)
		}
		return result, nil
	}

	requireReached := false
	if task.Type == models.TaskTypeDrop && newStatus == models.TaskStatusCompleted {
		cfg, err := s.loadReachedConfig(ctx)
		if err != nil {
			s.logger.WithError(err).Warn("reached config read failed; treating require_reached as false")
		} else {
			requireReached = cfg.RequireReached()
		}
	}
	if err := validateTaskTransition(*task, newStatus, requireReached); err != nil {
		return nil, op.Outcome("invalid_transition", err)
	}

	// Trip-status gate: pickup requires accepted, drop requires out_for_delivery.
	if err := validateTaskAgainstTripStatus(task.Type, trip.Status); err != nil {
		return nil, op.Outcome("prerequisite_incomplete", err)
	}

	// Drop completion requires the customer OTP.
	if task.Type == models.TaskTypeDrop && newStatus == models.TaskStatusCompleted {
		if err := validateDropOTP(*task, otp); err != nil {
			return nil, op.Outcome("invalid_otp", err)
		}
	}

	if photoS3Key != "" {
		task.PhotoS3Key = photoS3Key
	}

	// Rider pickup completion is gated on Java READY_FOR_DELIVERY so OFD is
	// not written while the store is still packing. Admin / skipJava paths
	// go through applyTaskCompletion directly and stay ungated.
	if task.Type == models.TaskTypePickup && newStatus == models.TaskStatusCompleted {
		if err := s.requirePacked(ctx, trip.OrderID); err != nil {
			return nil, op.Outcome("order_not_packed", err)
		}
	}

	if err := s.applyTaskCompletion(ctx, trip, task, de, newStatus, otp, "", false); err != nil {
		return nil, err
	}
	return &TaskUpdateResult{Status: "updated"}, nil
}

// AdminCompleteTask marks pickup or drop done for a driver's current trip.
// Admin-driven completion always skips OTP verification (pickup already had
// no OTP; drop's customer OTP is intentionally not checked here — ops must be
// able to close a drop without contacting the customer). Trip-status gating
// and task transition validation are unchanged. When adminUsername is
// non-empty the Java sync records the actor as ADMIN:{username} instead of
// DE:{deId} (see applyTaskCompletion / javaActor).
func (s *TripService) AdminCompleteTask(ctx context.Context, driverPhone string, taskType models.TaskType, adminUsername string) error {
	op := logging.Start(ctx, s.logger, "TripService.AdminCompleteTask", logrus.Fields{
		"phone": driverPhone, "task_type": string(taskType), "admin": adminUsername,
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

	task, err := adminSelectTask(trip, taskType)
	if err != nil {
		return op.Outcome("not_found", err)
	}

	if err := validateTaskTransition(*task, models.TaskStatusCompleted, false); err != nil {
		return op.Outcome("invalid_transition", err)
	}
	if err := validateTaskAgainstTripStatus(task.Type, trip.Status); err != nil {
		return op.Outcome("prerequisite_incomplete", err)
	}
	synthesizeAdminDropReached(task)

	return s.applyTaskCompletion(ctx, trip, task, de, models.TaskStatusCompleted, "", adminUsername, false)
}

// AdminCompleteDropByOrder force-delivers an order for admin "Mark Delivered".
// Mode comes from PreviewAdminDropByOrder: java-only status writes, assign a
// rider then complete, or force-progress the existing trip (pickup then drop).
func (s *TripService) AdminCompleteDropByOrder(ctx context.Context, orderID, adminUsername, driverPhone string) error {
	op := logging.Start(ctx, s.logger, "TripService.AdminCompleteDropByOrder", logrus.Fields{
		"order_id": orderID, "admin": adminUsername,
	})
	defer op.End()

	preview, err := s.PreviewAdminDropByOrder(ctx, orderID)
	if err != nil {
		return op.Fail(err)
	}

	skipJava := preview.JavaStatus == "DELIVERED"

	switch preview.Mode {
	case AdminDropModeBlocked:
		switch preview.Reason {
		case "java_cancelled":
			return op.Outcome("java_cancelled", ErrJavaOrderCancelled)
		case "java_not_ready":
			return op.Outcome("java_not_ready", ErrOrderNotDeliverable)
		case "rider_busy_elsewhere":
			return op.Outcome("rider_busy_elsewhere", ErrRiderBusyElsewhere)
		default:
			return op.Outcome("blocked", ErrOrderNotDeliverable)
		}
	case AdminDropModeAlreadyDone:
		return op.Outcome("already_delivered", ErrAlreadyDelivered)
	case AdminDropModeJavaOnly:
		if err := s.forceJavaDeliver(ctx, orderID, adminUsername); err != nil {
			return op.Fail(err)
		}
		return nil
	case AdminDropModePickRider:
		if strings.TrimSpace(driverPhone) == "" {
			return op.Outcome("rider_required", ErrRiderRequired)
		}
		if err := s.forceAssignAndComplete(ctx, orderID, adminUsername, driverPhone, skipJava); err != nil {
			return op.Fail(err)
		}
		return nil
	case AdminDropModeForceProgress:
		if err := s.forceProgressExisting(ctx, orderID, adminUsername, skipJava); err != nil {
			return op.Fail(err)
		}
		return nil
	default:
		return op.Outcome("unknown_mode", ErrOrderNotDeliverable)
	}
}

type adminPickupMode string

const (
	adminPickupJavaOnly      adminPickupMode = "java_only"
	adminPickupForceProgress adminPickupMode = "force_progress"
	adminPickupAlreadyDone   adminPickupMode = "already_done"
	adminPickupBlocked       adminPickupMode = "blocked"
)

type adminPickupClass struct {
	Mode       adminPickupMode
	Reason     string
	JavaStatus string
}

func (s *TripService) classifyAdminPickupByOrder(ctx context.Context, orderID string) (*adminPickupClass, error) {
	javaStatus := ""
	if s.javaClient != nil {
		status, err := s.javaClient.GetOrderStatus(ctx, orderID)
		if err != nil {
			return nil, err
		}
		if status != "NOT_FOUND" {
			javaStatus = status
		}
	}

	if javaStatus == "CANCELLED" {
		return &adminPickupClass{Mode: adminPickupBlocked, Reason: "java_cancelled", JavaStatus: javaStatus}, nil
	}

	trip, err := s.tripRepo.GetByOrderID(ctx, orderID)
	if err != nil {
		return nil, err
	}

	if trip == nil || !pickupTripConsideredOpen(trip) || trip.Status == models.TripStatusCreated || trip.DEPhone == "" {
		return missingOrNoDriverPickup(javaStatus), nil
	}
	if trip.Status == models.TripStatusOutForDelivery {
		return missingOrNoDriverPickup(javaStatus), nil
	}

	if javaStatus == "OUT_FOR_DELIVERY" || javaStatus == "DELIVERED" {
		return &adminPickupClass{Mode: adminPickupAlreadyDone, JavaStatus: javaStatus}, nil
	}

	de, err := s.deRepo.GetByPhone(ctx, trip.DEPhone)
	if err != nil {
		return nil, err
	}
	if de != nil && de.Status == models.DEStatusBusy && de.CurrentOrderID != "" && de.CurrentOrderID != trip.OrderID {
		return &adminPickupClass{Mode: adminPickupBlocked, Reason: "rider_busy_elsewhere", JavaStatus: javaStatus}, nil
	}

	return &adminPickupClass{Mode: adminPickupForceProgress, JavaStatus: javaStatus}, nil
}

func pickupTripConsideredOpen(trip *models.Trip) bool {
	switch trip.Status {
	case models.TripStatusAssigned, models.TripStatusAccepted, models.TripStatusOutForDelivery:
		return true
	default:
		return false
	}
}

func missingOrNoDriverPickup(javaStatus string) *adminPickupClass {
	switch javaStatus {
	case "OUT_FOR_DELIVERY", "DELIVERED":
		return &adminPickupClass{Mode: adminPickupAlreadyDone, JavaStatus: javaStatus}
	case "READY_FOR_DELIVERY":
		return &adminPickupClass{Mode: adminPickupJavaOnly, JavaStatus: javaStatus}
	default:
		return &adminPickupClass{Mode: adminPickupBlocked, Reason: "java_not_ready", JavaStatus: javaStatus}
	}
}

// AdminCompletePickupByOrder advances an order to OUT_FOR_DELIVERY for admin
// "Advance to OUT FOR DELIVERY". Mode comes from classifyAdminPickupByOrder:
// java-only status write, force-progress the existing trip's pickup, already
// done, or blocked. There is no pick-rider path and no preview GET.
func (s *TripService) AdminCompletePickupByOrder(ctx context.Context, orderID, adminUsername string) error {
	op := logging.Start(ctx, s.logger, "TripService.AdminCompletePickupByOrder", logrus.Fields{
		"order_id": orderID, "admin": adminUsername,
	})
	defer op.End()

	class, err := s.classifyAdminPickupByOrder(ctx, orderID)
	if err != nil {
		return op.Fail(err)
	}

	switch class.Mode {
	case adminPickupBlocked:
		switch class.Reason {
		case "java_cancelled":
			return op.Outcome("java_cancelled", ErrJavaOrderCancelled)
		case "java_not_ready":
			return op.Outcome("java_not_ready", ErrOrderNotDeliverable)
		case "rider_busy_elsewhere":
			return op.Outcome("rider_busy_elsewhere", ErrRiderBusyElsewhere)
		default:
			return op.Outcome("blocked", ErrOrderNotDeliverable)
		}
	case adminPickupAlreadyDone:
		return op.Outcome("already_ofd", ErrAlreadyOutForDelivery)
	case adminPickupJavaOnly:
		if err := s.forceJavaOutForDelivery(ctx, orderID, adminUsername); err != nil {
			return op.Fail(err)
		}
		return nil
	case adminPickupForceProgress:
		if err := s.forceProgressPickup(ctx, orderID, adminUsername, class.JavaStatus); err != nil {
			return op.Fail(err)
		}
		return nil
	default:
		return op.Outcome("unknown_mode", ErrOrderNotDeliverable)
	}
}

type AdminDropMode string

const (
	AdminDropModeJavaOnly      AdminDropMode = "java_only"
	AdminDropModePickRider     AdminDropMode = "pick_rider"
	AdminDropModeForceProgress AdminDropMode = "force_progress"
	AdminDropModeAlreadyDone   AdminDropMode = "already_done"
	AdminDropModeBlocked       AdminDropMode = "blocked"
)

type AdminDropCandidate struct {
	Phone         string  `json:"phone"`
	Name          string  `json:"name"`
	Status        string  `json:"status"`
	InHandCashZMW float64 `json:"in_hand_cash_zmw"`
}

type AdminDropRider struct {
	Phone  string `json:"phone"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type AdminDropPreview struct {
	Mode       AdminDropMode        `json:"mode"`
	Reason     string               `json:"reason,omitempty"`
	TripID     string               `json:"trip_id,omitempty"`
	JavaStatus string               `json:"java_status,omitempty"`
	Rider      *AdminDropRider      `json:"rider,omitempty"`
	Candidates []AdminDropCandidate `json:"candidates,omitempty"`
}

// PreviewAdminDropByOrder classifies how an admin can force-deliver an order
// without mutating trip, rider, or Java state.
func (s *TripService) PreviewAdminDropByOrder(ctx context.Context, orderID string) (*AdminDropPreview, error) {
	javaStatus := ""
	if s.javaClient != nil {
		status, err := s.javaClient.GetOrderStatus(ctx, orderID)
		if err != nil {
			return nil, err
		}
		if status != "NOT_FOUND" {
			javaStatus = status
		}
	}

	if javaStatus == "CANCELLED" {
		return &AdminDropPreview{Mode: AdminDropModeBlocked, Reason: "java_cancelled", JavaStatus: javaStatus}, nil
	}

	trip, err := s.tripRepo.GetByOrderID(ctx, orderID)
	if err != nil {
		return nil, err
	}

	if trip == nil || !adminDropTripOpen(trip.Status) {
		return previewMissingOrClosedTrip(javaStatus), nil
	}

	if trip.DEPhone == "" {
		candidates, err := s.listAdminDropCandidates(ctx, trip.StoreID)
		if err != nil {
			return nil, err
		}
		return &AdminDropPreview{
			Mode:       AdminDropModePickRider,
			TripID:     trip.TripID,
			JavaStatus: javaStatus,
			Candidates: candidates,
		}, nil
	}

	de, err := s.deRepo.GetByPhone(ctx, trip.DEPhone)
	if err != nil {
		return nil, err
	}
	rider := adminDropRiderFrom(de)
	if de != nil && de.Status == models.DEStatusBusy && de.CurrentOrderID != "" && de.CurrentOrderID != trip.OrderID {
		return &AdminDropPreview{
			Mode:       AdminDropModeBlocked,
			Reason:     "rider_busy_elsewhere",
			TripID:     trip.TripID,
			Rider:      rider,
			JavaStatus: javaStatus,
		}, nil
	}
	return &AdminDropPreview{
		Mode:       AdminDropModeForceProgress,
		TripID:     trip.TripID,
		Rider:      rider,
		JavaStatus: javaStatus,
	}, nil
}

func previewMissingOrClosedTrip(javaStatus string) *AdminDropPreview {
	switch javaStatus {
	case "DELIVERED":
		return &AdminDropPreview{Mode: AdminDropModeAlreadyDone, JavaStatus: javaStatus}
	case "READY_FOR_DELIVERY", "OUT_FOR_DELIVERY":
		return &AdminDropPreview{Mode: AdminDropModeJavaOnly, JavaStatus: javaStatus}
	default:
		return &AdminDropPreview{Mode: AdminDropModeBlocked, Reason: "java_not_ready", JavaStatus: javaStatus}
	}
}

func adminDropTripOpen(status models.TripStatus) bool {
	switch status {
	case models.TripStatusCreated, models.TripStatusAssigned, models.TripStatusAccepted, models.TripStatusOutForDelivery:
		return true
	default:
		return false
	}
}

func adminDropRiderFrom(de *models.DeliveryExecutive) *AdminDropRider {
	if de == nil {
		return nil
	}
	return &AdminDropRider{Phone: de.PhoneNumber, Name: de.Name, Status: string(de.Status)}
}

func (s *TripService) listAdminDropCandidates(ctx context.Context, storeID string) ([]AdminDropCandidate, error) {
	indexKey := models.AssignedStoreIndexKeyFor(storeID)
	var out []AdminDropCandidate
	cursor := ""
	for {
		des, next, err := s.deRepo.ListByAssignedStore(ctx, indexKey, "", cursor, 100)
		if err != nil {
			return nil, err
		}
		for _, de := range des {
			if de == nil {
				continue
			}
			switch de.Status {
			case models.DEStatusOffline, models.DEStatusEligible, models.DEStatusFree:
				out = append(out, AdminDropCandidate{
					Phone:         de.PhoneNumber,
					Name:          de.Name,
					Status:        string(de.Status),
					InHandCashZMW: de.InHandCashZMW,
				})
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return out, nil
}

func (s *TripService) forceJavaDeliver(ctx context.Context, orderID, adminUsername string) error {
	if s.javaClient == nil {
		return ErrOrderNotDeliverable
	}
	st, _ := s.javaClient.GetOrderStatus(ctx, orderID)
	actor := "ADMIN:" + adminUsername
	switch st {
	case "DELIVERED":
		return nil
	case "READY_FOR_DELIVERY":
		if err := s.javaClient.UpdateOrderStatus(ctx, orderID, "OUT_FOR_DELIVERY", actor); err != nil {
			return err
		}
		return s.javaClient.UpdateOrderStatus(ctx, orderID, "DELIVERED", actor)
	case "OUT_FOR_DELIVERY":
		return s.javaClient.UpdateOrderStatus(ctx, orderID, "DELIVERED", actor)
	default:
		return ErrOrderNotDeliverable
	}
}

func (s *TripService) forceJavaOutForDelivery(ctx context.Context, orderID, adminUsername string) error {
	if s.javaClient == nil {
		return ErrOrderNotDeliverable
	}
	st, _ := s.javaClient.GetOrderStatus(ctx, orderID)
	actor := "ADMIN:" + adminUsername
	switch st {
	case "READY_FOR_DELIVERY":
		return s.javaClient.UpdateOrderStatus(ctx, orderID, "OUT_FOR_DELIVERY", actor)
	case "OUT_FOR_DELIVERY", "DELIVERED":
		return nil
	case "CANCELLED":
		return ErrJavaOrderCancelled
	default:
		return ErrOrderNotDeliverable
	}
}

func (s *TripService) forceAssignAndComplete(ctx context.Context, orderID, adminUsername, driverPhone string, skipJava bool) (err error) {
	de, err := s.deRepo.GetByPhone(ctx, driverPhone)
	if err != nil {
		return err
	}
	if de == nil {
		return fmt.Errorf("%w: %s", ErrDENotFound, driverPhone)
	}
	if de.Status == models.DEStatusBusy {
		return ErrRiderBusyElsewhere
	}

	trip, err := s.tripRepo.GetByOrderID(ctx, orderID)
	if err != nil {
		return err
	}
	if trip == nil {
		return fmt.Errorf("%w: no trip for order %s", ErrTripNotFound, orderID)
	}

	prior := de.Status
	flipped := false
	if de.Status != models.DEStatusEligible {
		if uerr := s.deRepo.UpdateStatus(ctx, de.PhoneNumber, models.DEStatusEligible, trip.StoreID, ""); uerr != nil {
			return uerr
		}
		flipped = true
	}
	// The offline->eligible flip above makes the rider visible to the
	// assignment cron. If anything after it fails, restore the rider's prior
	// status so a failed force-assign never strands an offline rider on duty.
	defer func() {
		if err != nil && flipped {
			if rerr := s.deRepo.UpdateStatus(ctx, de.PhoneNumber, prior, "", ""); rerr != nil {
				s.logger.WithError(rerr).WithField("phone", de.PhoneNumber).
					Error("force-assign rollback failed; rider may be left on duty")
			}
		}
	}()

	if aerr := s.tripRepo.AdminAssign(ctx, trip.TripID, trip.OrderID, de.DEID, de.PhoneNumber, trip.StoreID); aerr != nil {
		// A condition-failure (trip no longer `created` / DE no longer
		// `eligible`) is a lost race, not a server fault — surface it as a 409.
		if errors.Is(aerr, repository.ErrAdminAssignConflict) {
			return fmt.Errorf("%w: %v", ErrForceAssignConflict, aerr)
		}
		return aerr
	}
	trip, err = s.tripRepo.GetByOrderID(ctx, orderID)
	if err != nil {
		return err
	}
	if trip == nil {
		return fmt.Errorf("%w: no trip for order %s", ErrTripNotFound, orderID)
	}
	if cerr := s.completePickupThenDrop(ctx, trip, de, adminUsername, skipJava); cerr != nil {
		return cerr
	}
	if prior == models.DEStatusOffline {
		if uerr := s.deRepo.UpdateStatus(ctx, de.PhoneNumber, models.DEStatusOffline, "", ""); uerr != nil {
			return uerr
		}
	}
	return nil
}

func (s *TripService) forceProgressExisting(ctx context.Context, orderID, adminUsername string, skipJava bool) error {
	trip, err := s.tripRepo.GetByOrderID(ctx, orderID)
	if err != nil {
		return err
	}
	if trip == nil {
		return fmt.Errorf("%w: no trip for order %s", ErrTripNotFound, orderID)
	}
	de, err := s.deRepo.GetByPhone(ctx, trip.DEPhone)
	if err != nil {
		return err
	}
	if de == nil {
		return fmt.Errorf("%w: %s", ErrDENotFound, trip.DEPhone)
	}
	if de.Status == models.DEStatusBusy && de.CurrentOrderID != "" && de.CurrentOrderID != trip.OrderID {
		return ErrRiderBusyElsewhere
	}

	prior := de.Status
	if de.CurrentOrderID != trip.OrderID || de.Status != models.DEStatusBusy {
		if err := s.deRepo.AttachToTrip(ctx, de.PhoneNumber, trip.OrderID, trip.TripID, trip.StoreID); err != nil {
			return err
		}
	}
	if trip.Status == models.TripStatusAssigned {
		if err := s.tripRepo.Accept(ctx, trip.TripID, de.DEID); err != nil {
			return err
		}
		trip.Status = models.TripStatusAccepted
	}
	if err := s.completePickupThenDrop(ctx, trip, de, adminUsername, skipJava); err != nil {
		return err
	}
	if prior == models.DEStatusOffline {
		if err := s.deRepo.UpdateStatus(ctx, de.PhoneNumber, models.DEStatusOffline, "", ""); err != nil {
			return err
		}
	}
	return nil
}

func (s *TripService) forceProgressPickup(ctx context.Context, orderID, adminUsername, javaStatus string) error {
	trip, err := s.tripRepo.GetByOrderID(ctx, orderID)
	if err != nil {
		return err
	}
	if trip == nil {
		return fmt.Errorf("%w: no trip for order %s", ErrTripNotFound, orderID)
	}
	de, err := s.deRepo.GetByPhone(ctx, trip.DEPhone)
	if err != nil {
		return err
	}
	if de == nil {
		return fmt.Errorf("%w: %s", ErrDENotFound, trip.DEPhone)
	}
	if de.Status == models.DEStatusBusy && de.CurrentOrderID != "" && de.CurrentOrderID != trip.OrderID {
		return ErrRiderBusyElsewhere
	}

	if de.CurrentOrderID != trip.OrderID || de.Status != models.DEStatusBusy {
		if err := s.deRepo.AttachToTrip(ctx, de.PhoneNumber, trip.OrderID, trip.TripID, trip.StoreID); err != nil {
			return err
		}
	}
	if trip.Status == models.TripStatusAssigned {
		if err := s.tripRepo.Accept(ctx, trip.TripID, de.DEID); err != nil {
			return err
		}
		trip.Status = models.TripStatusAccepted
	}

	pickup := trip.PickupTask()
	if pickup != nil && pickup.Status != models.TaskStatusCompleted {
		if err := validateTaskTransition(*pickup, models.TaskStatusCompleted, false); err != nil {
			return err
		}
		if err := s.applyTaskCompletion(ctx, trip, pickup, de, models.TaskStatusCompleted, "", adminUsername, true); err != nil {
			return err
		}
	}

	if javaStatus == "OUT_FOR_DELIVERY" || javaStatus == "DELIVERED" {
		return nil
	}
	return s.forceJavaOutForDelivery(ctx, orderID, adminUsername)
}

// completePickupThenDrop force-completes the pickup (if needed) then the drop
// on an existing trip. Java is never synced through applyTaskCompletion here:
// its async syncJavaWithRetry would fire two unordered goroutines
// (OUT_FOR_DELIVERY and DELIVERED) that can race and whose failures are
// silent. Instead the trip writes (COD, payout, push, trip close) run with
// skipJava=true, and Java is walked once, synchronously and in order, via
// forceJavaDeliver after the trip work — so the two writes cannot race and a
// Java refusal fails the POST (the same guarantee as the no-trip path). When
// Java is already DELIVERED (javaAlreadyDelivered), Java is left untouched.
func (s *TripService) completePickupThenDrop(ctx context.Context, trip *models.Trip, de *models.DeliveryExecutive, adminUsername string, javaAlreadyDelivered bool) error {
	pickup := trip.PickupTask()
	if pickup != nil && pickup.Status != models.TaskStatusCompleted {
		if trip.Status != models.TripStatusAccepted {
			if err := s.tripRepo.Accept(ctx, trip.TripID, de.DEID); err != nil {
				return err
			}
			trip.Status = models.TripStatusAccepted
		}
		if err := validateTaskTransition(*pickup, models.TaskStatusCompleted, false); err != nil {
			return err
		}
		if err := s.applyTaskCompletion(ctx, trip, pickup, de, models.TaskStatusCompleted, "", adminUsername, true); err != nil {
			return err
		}
	}
	trip.Status = models.TripStatusOutForDelivery

	drop, err := adminSelectTask(trip, models.TaskTypeDrop)
	if err != nil {
		return err
	}
	if err := validateTaskTransition(*drop, models.TaskStatusCompleted, false); err != nil {
		return err
	}
	synthesizeAdminDropReached(drop)
	if err := s.applyTaskCompletion(ctx, trip, drop, de, models.TaskStatusCompleted, "", adminUsername, true); err != nil {
		return err
	}

	if javaAlreadyDelivered {
		return nil
	}
	return s.forceJavaDeliver(ctx, trip.OrderID, adminUsername)
}

// adminSelectTask resolves the pickup/drop task an admin completion targets.
func adminSelectTask(trip *models.Trip, taskType models.TaskType) (*models.Task, error) {
	var task *models.Task
	switch taskType {
	case models.TaskTypePickup:
		task = trip.PickupTask()
	case models.TaskTypeDrop:
		task = trip.DropTask()
	default:
		return nil, fmt.Errorf("%w: unsupported task type", ErrTaskNotFound)
	}
	if task == nil {
		return nil, fmt.Errorf("%w: %s task missing", ErrTaskNotFound, taskType)
	}
	return task, nil
}

// javaActor formats the actor string recorded against a Java order-status
// sync. Admin-driven completions (adminUsername non-empty) record
// ADMIN:{username}; rider-driven completions record DE:{deId}.
func javaActor(de *models.DeliveryExecutive, adminUsername string) string {
	if adminUsername != "" {
		return "ADMIN:" + adminUsername
	}
	return "DE:" + de.DEID
}

func (s *TripService) loadReachedConfig(ctx context.Context) (*models.TripReachedConfig, error) {
	if s.reachedConfig == nil {
		return &models.TripReachedConfig{}, nil
	}
	return s.reachedConfig.Get(ctx)
}

func validateDriverCoords(lat, lng *float64) error {
	if lat == nil || lng == nil {
		return fmt.Errorf("%w", ErrMissingLocation)
	}
	if math.IsNaN(*lat) || math.IsNaN(*lng) || math.IsInf(*lat, 0) || math.IsInf(*lng, 0) {
		return fmt.Errorf("%w", ErrInvalidCoordinates)
	}
	if *lat < -90 || *lat > 90 || *lng < -180 || *lng > 180 {
		return fmt.Errorf("%w", ErrInvalidCoordinates)
	}
	return nil
}

func customerCoordsUsable(task models.Task) bool {
	return !(task.Lat == 0 && task.Lng == 0)
}

// applyDropReached soft-geofences the driver against the drop task, then
// persists reached on first tap only. It never calls Java, payout, or notify.
func (s *TripService) applyDropReached(ctx context.Context, trip *models.Trip, task *models.Task, lat, lng float64) (*TaskUpdateResult, error) {
	cfg, err := s.loadReachedConfig(ctx)
	if err != nil {
		return nil, err
	}
	radius := cfg.EffectiveRadiusMeters()
	result := &TaskUpdateResult{Status: "updated"}

	if customerCoordsUsable(*task) {
		dist := models.HaversineDistance(lat, lng, task.Lat, task.Lng)
		within := dist <= radius
		result.DistanceMeters = &dist
		result.RadiusMeters = &radius
		result.WithinRadius = &within
		s.logger.WithFields(logrus.Fields{
			"distance_meters": dist,
			"radius_meters":   radius,
			"within_radius":   within,
			"trip_id":         trip.TripID,
		}).Info("drop reached geofence")
	} else {
		s.logger.WithField("trip_id", trip.TripID).Info("drop reached: customer coords missing, skipping distance")
	}

	if task.Status != models.TaskStatusReached {
		task.Status = models.TaskStatusReached
		task.ReachedAt = timezone.Now().Format(time.RFC3339)
		if err := s.tripRepo.UpdateTasks(ctx, trip.TripID, trip.Tasks); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// synthesizeAdminDropReached marks a created drop as reached (setting ReachedAt
// only if empty) so admin complete can proceed from reached without OTP or geofence.
func synthesizeAdminDropReached(task *models.Task) {
	if task.Type != models.TaskTypeDrop || task.Status != models.TaskStatusCreated {
		return
	}
	task.Status = models.TaskStatusReached
	if task.ReachedAt == "" {
		task.ReachedAt = timezone.Now().Format(time.RFC3339)
	}
}

func (s *TripService) applyTaskCompletion(ctx context.Context, trip *models.Trip, task *models.Task, de *models.DeliveryExecutive, newStatus models.TaskStatus, otp, adminUsername string, skipJava bool) error {
	task.Status = newStatus
	if (task.Type == models.TaskTypePickup || task.Type == models.TaskTypeDrop) && newStatus == models.TaskStatusCompleted {
		task.CompletedAt = timezone.Now().Format(time.RFC3339)
	}

	actor := javaActor(de, adminUsername)

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
		if !skipJava {
			go s.syncJavaWithRetry(trip.OrderID, "DELIVERED", actor)
		}
		s.notifyCustomer(trip, de, eventDelivered)
		s.recordTripPayout(trip, de)
		return nil
	}

	if err := s.tripRepo.UpdateTasks(ctx, trip.TripID, trip.Tasks); err != nil {
		return err
	}

	s.onTaskCompleted(ctx, trip, task, de, actor, skipJava)
	return nil
}

// onTaskCompleted updates trip status and asynchronously syncs Java when needed.
func (s *TripService) onTaskCompleted(ctx context.Context, trip *models.Trip, task *models.Task, de *models.DeliveryExecutive, actor string, skipJava bool) {
	// Use a detached context for all writes — the request context may be cancelled
	// if the client disconnects before these writes complete.
	bgCtx := context.Background()

	switch {
	case task.Type == models.TaskTypePickup && task.Status == models.TaskStatusCompleted:
		var cfg *models.DropDeadlineConfig
		if s.dropDeadlineConfig != nil {
			var err error
			cfg, err = s.dropDeadlineConfig.Get(bgCtx)
			if err != nil {
				s.logger.WithError(err).WithField("trip_id", trip.TripID).
					Error("failed to load drop-deadline config; using defaults")
				cfg = nil
			}
		}
		deadline := models.ComputeDropDeadlineUnix(timezone.Now(), trip.DistanceKM, cfg.EffectiveMinutesPerKm(), cfg.EffectiveExtraMinutes())
		if err := s.tripRepo.MarkOutForDelivery(bgCtx, trip.TripID, deadline); err != nil {
			s.logger.WithError(err).WithField("trip_id", trip.TripID).Error("failed to mirror trip status")
		}
		if !skipJava {
			go s.syncJavaWithRetry(trip.OrderID, "OUT_FOR_DELIVERY", actor)
		}
		s.notifyCustomer(trip, de, eventOutForDelivery)

	}
}

// notifyCustomer pushes a driver-triggered order update to the customer.
//
// Resolves the recipient from trip.CustomerUserID, which the assignment cron
// sets at trip creation. It deliberately does NOT call the Java order-service:
// the /internal/v1/orders/{ref}/notification-target endpoint this used to
// depend on was never implemented, which silently killed every customer push.
//
// Dispatches on a goroutine so the driver's swipe-to-confirm is not delayed by
// the FCM round-trip. Payload construction is synchronous and pure, so the
// interesting logic stays testable.
func (s *TripService) notifyCustomer(trip *models.Trip, de *models.DeliveryExecutive, event customerOrderEvent) {
	if s.notifier == nil {
		s.logger.WithFields(logrus.Fields{
			"order_id": tripOrderID(trip),
			"event":    string(event),
		}).Debug("customer push skipped — notifier not configured")
		return
	}

	req, ok := buildCustomerNotification(trip, de, event)
	if !ok {
		s.logger.WithFields(logrus.Fields{
			"order_id": tripOrderID(trip),
			"event":    string(event),
		}).Warn("customer push skipped — nothing to send")
		return
	}

	// Captured before the goroutine so the outcome log does not depend on the
	// wire-format "order_number" key inside req.Data, which exists only to
	// satisfy the mobile app's deep-link payload and could be renamed
	// independently of this log.
	orderNumber := tripOrderID(trip)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		res := s.notifier.Send(ctx, req)
		fields := logrus.Fields{
			"order_id":     orderNumber,
			"event":        req.EventType,
			"recipient_id": req.RecipientID,
			"status":       string(res.Status),
		}
		if res.Reason != "" {
			fields["reason"] = res.Reason
		}
		// Logged at Info on every outcome — including "skipped" — because the
		// previous version discarded this result, which is how a completely
		// broken push pipeline went unnoticed in production.
		s.logger.WithFields(fields).Info("customer push outcome")
	}()
}

// tripOrderID safely reads the order id for logging on the nil-trip path.
func tripOrderID(trip *models.Trip) string {
	if trip == nil {
		return ""
	}
	return trip.OrderID
}

// syncJavaWithRetry retries the Java status update up to 3 times with backoff.
// Runs in a goroutine — does not block the DE response. actor is the fully
// formatted Java actor string (e.g. "DE:{deId}" or "ADMIN:{username}"); see
// javaActor.
func (s *TripService) syncJavaWithRetry(orderID, status, actor string) {
	if s.javaClient == nil {
		return
	}
	ctx := context.Background()
	backoff := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}
	for i, delay := range backoff {
		if err := s.javaClient.UpdateOrderStatus(ctx, orderID, status, actor); err != nil {
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

// validateTaskTransition enforces drop created→reached→completed (and the
// compat created→completed path when requireReached is false). Pickup only
// validates created→completed; pickup target reached is no-op'd by the
// service before this helper is called.
func validateTaskTransition(task models.Task, newStatus models.TaskStatus, requireReached bool) error {
	if task.Status == models.TaskStatusCompleted {
		return fmt.Errorf("%w: task is already completed", ErrInvalidTransition)
	}
	if newStatus == models.TaskStatusReached {
		if task.Type == models.TaskTypeDrop && task.Status == models.TaskStatusCreated {
			return nil
		}
		return fmt.Errorf("%w: invalid transition to reached", ErrInvalidTransition)
	}
	if newStatus != models.TaskStatusCompleted {
		return fmt.Errorf("%w: only transition to completed is allowed", ErrInvalidTransition)
	}
	if requireReached && task.Type == models.TaskTypeDrop && task.Status == models.TaskStatusCreated {
		return fmt.Errorf("%w", ErrDropNotReached)
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
// Rounded to 2dp so DynamoDB never stores float64 binary noise.
func codAccrualAmount(trip *models.Trip) float64 {
	if trip.Payment != nil && trip.Payment.CollectCash {
		return money.Round2ZMW(trip.Payment.AmountZMW)
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

// requirePacked ensures the Java order is READY_FOR_DELIVERY before rider
// pickup verification or completion. A nil javaClient fails closed.
func (s *TripService) requirePacked(ctx context.Context, orderID string) error {
	if s.javaClient == nil {
		return ErrOrderNotPacked
	}
	status, err := s.javaClient.GetOrderStatus(ctx, orderID)
	if err != nil {
		return err
	}
	if status != eligibleOrderStatus {
		return ErrOrderNotPacked
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
	if err := s.requirePacked(ctx, trip.OrderID); err != nil {
		return op.Outcome("order_not_packed", err)
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
