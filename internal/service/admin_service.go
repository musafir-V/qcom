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

var (
	ErrDENotFound          = errors.New("driver not found")
	ErrDENotEligible       = errors.New("driver not eligible")
	ErrTripNotReassignable = errors.New("trip is not in a reassignable state")
	ErrSameDriver          = errors.New("trip is already assigned to this driver")
	ErrDriverWrongStore    = errors.New("driver is not on duty at this trip's store")
	ErrInvalidReasonCode   = errors.New("invalid reassignment reason code")
	// ErrReassignConflict surfaces a lost race on the reassign transaction —
	// the trip, outgoing rider, or incoming rider changed underneath the
	// request (e.g. the assignment cron won first). Callers map this to 409;
	// the correct client action is refresh and retry, not "the system is broken".
	ErrReassignConflict = errors.New("reassign conflict: refresh and retry")
)

// adminTripRepo is the subset of TripRepository AdminService uses. Narrowing to
// an interface lets the reassignment tests inject stubs without DynamoDB.
type adminTripRepo interface {
	GetByID(ctx context.Context, tripID string) (*models.Trip, error)
	GetByOrderID(ctx context.Context, orderID string) (*models.Trip, error)
	AdminAssign(ctx context.Context, tripID, orderID, deID, dePhone, storeID string) error
	Reassign(ctx context.Context, tripID, fromDEPhone, fromDEID, toDEID, toDEPhone, orderID, storeID string,
		promoteToAccepted bool, entry models.TripReassignment) error
}

// adminDERepo is the subset of DERepository AdminService uses.
type adminDERepo interface {
	GetByPhone(ctx context.Context, phone string) (*models.DeliveryExecutive, error)
	FindOnDutyByStore(ctx context.Context, storeID string) ([]*models.DeliveryExecutive, error)
}

// AdminService force-assigns pooled orders to drivers and reassigns in-flight
// trips between riders (ops escape hatches). Auth is handled upstream.
type AdminService struct {
	tripRepo        adminTripRepo
	deRepo          adminDERepo
	cashConfigRepo  *repository.CashConfigRepository
	statusEventRepo statusEventAppender
	notifier        NotificationService
	logger          *logrus.Logger
	completer       pickupAutoCompleter
}

func NewAdminService(
	tripRepo *repository.TripRepository,
	deRepo *repository.DERepository,
	cashConfigRepo *repository.CashConfigRepository,
	statusEventRepo *repository.DEStatusEventRepository,
	notifier NotificationService,
	logger *logrus.Logger,
	completer pickupAutoCompleter,
) *AdminService {
	return &AdminService{
		tripRepo:        tripRepo,
		deRepo:          deRepo,
		cashConfigRepo:  cashConfigRepo,
		statusEventRepo: statusEventRepo,
		notifier:        notifier,
		logger:          logger,
		completer:       completer,
	}
}

// AssignOrder force-assigns the trip for orderID directly to accepted for the
// driver identified by driverPhone. Preconditions: trip is created (pooled),
// driver is eligible (online + on duty, not busy). Bypasses rejected_de_ids.
func (s *AdminService) AssignOrder(ctx context.Context, orderID, driverPhone string) error {
	op := logging.Start(ctx, s.logger, "AdminService.AssignOrder", logrus.Fields{
		"order_id": orderID, "driver_phone": driverPhone,
	})
	defer op.End()

	trip, err := s.tripRepo.GetByOrderID(ctx, orderID)
	if err != nil {
		return op.Fail(err)
	}
	if trip == nil {
		return op.Outcome("not_found", fmt.Errorf("%w: order %s", ErrTripNotFound, orderID))
	}
	if trip.Status != models.TripStatusCreated {
		return op.Outcome("not_assignable", fmt.Errorf("%w: order not in pool (status=%s)", ErrInvalidTripTransition, trip.Status))
	}

	de, err := s.deRepo.GetByPhone(ctx, driverPhone)
	if err != nil {
		return op.Fail(err)
	}
	if de == nil {
		return op.Outcome("de_not_found", fmt.Errorf("%w: %s", ErrDENotFound, driverPhone))
	}
	if de.Status != models.DEStatusEligible {
		return op.Outcome("de_not_eligible", fmt.Errorf("%w: driver status=%s", ErrDENotEligible, de.Status))
	}

	if err := s.tripRepo.AdminAssign(ctx, trip.TripID, trip.OrderID, de.DEID, de.PhoneNumber, trip.StoreID); err != nil {
		return op.Fail(err)
	}
	if s.completer != nil {
		if cerr := s.completer.AutoCompletePickupIfJavaOFD(ctx, orderID); cerr != nil {
			s.logger.WithError(cerr).WithField("order_id", orderID).
				Warn("AssignOrder: auto-complete pickup failed")
		}
	}
	return nil
}

// isReassignableStatus reports whether a trip state may be moved to another rider.
func isReassignableStatus(s models.TripStatus) bool {
	return s == models.TripStatusAssigned ||
		s == models.TripStatusAccepted ||
		s == models.TripStatusOutForDelivery
}

// ReassignCandidate is a rider the admin may hand a trip to.
type ReassignCandidate struct {
	DEID           string  `json:"de_id"`
	PhoneNumber    string  `json:"phone_number"`
	Name           string  `json:"name"`
	Status         string  `json:"status"`
	InHandCashZMW  float64 `json:"in_hand_cash_zmw"`
	CashOverLimit  bool    `json:"cash_over_limit"`
	PreviouslyHeld bool    `json:"previously_held"`
}

// ReassignCandidates lists riders on duty at the trip's store who could take it:
// status eligible or free, excluding the current holder. Riders over the cash cap
// are flagged, not filtered — the cap is advisory here because a customer is
// already waiting. Previous holders are flagged so reselecting one (the undo
// path) is a deliberate choice.
func (s *AdminService) ReassignCandidates(ctx context.Context, tripID string) ([]ReassignCandidate, error) {
	op := logging.Start(ctx, s.logger, "AdminService.ReassignCandidates", logrus.Fields{"trip_id": tripID})
	defer op.End()

	trip, err := s.tripRepo.GetByID(ctx, tripID)
	if err != nil {
		return nil, op.Fail(err)
	}
	if trip == nil {
		return nil, op.Outcome("not_found", fmt.Errorf("%w: %s", ErrTripNotFound, tripID))
	}
	if !isReassignableStatus(trip.Status) {
		return nil, op.Outcome("not_reassignable", fmt.Errorf(
			"%w: status=%s (use POST /admin/assign for pooled trips)", ErrTripNotReassignable, trip.Status))
	}

	des, err := s.deRepo.FindOnDutyByStore(ctx, trip.StoreID)
	if err != nil {
		return nil, op.Fail(err)
	}

	// Cash limit is display-only; a lookup failure must not fail the request.
	cashLimit := (&models.CashConfig{}).EffectiveLimitZMW()
	if s.cashConfigRepo != nil {
		if cfg, cErr := s.cashConfigRepo.Get(ctx); cErr != nil {
			s.logger.WithError(cErr).Warn("reassign: cash config lookup failed; using default limit")
		} else {
			cashLimit = cfg.EffectiveLimitZMW()
		}
	}

	held := map[string]bool{}
	for _, id := range trip.RejectedDEIDs {
		held[id] = true
	}
	for _, r := range trip.Reassignments {
		held[r.FromDEID] = true
		held[r.ToDEID] = true
	}

	out := make([]ReassignCandidate, 0, len(des))
	for _, de := range des {
		// Same guard shape as ReassignTrip (de_id OR phone), so the UI can never
		// offer a rider that ReassignTrip would then reject with ErrSameDriver.
		if de.DEID == trip.DEID || de.PhoneNumber == trip.DEPhone {
			continue
		}
		if de.Status != models.DEStatusEligible && de.Status != models.DEStatusFree {
			continue
		}
		out = append(out, ReassignCandidate{
			DEID:           de.DEID,
			PhoneNumber:    de.PhoneNumber,
			Name:           de.Name,
			Status:         string(de.Status),
			InHandCashZMW:  de.InHandCashZMW,
			CashOverLimit:  de.CashExceeds(cashLimit),
			PreviouslyHeld: held[de.DEID],
		})
	}
	op.With("count", len(out))
	return out, nil
}

// ReassignTrip moves an in-flight trip from its current rider to toDEPhone.
// Tasks and payout stamps are untouched; the outgoing rider is released to free
// and earns nothing for the partial trip.
func (s *AdminService) ReassignTrip(ctx context.Context, tripID, toDEPhone, reasonCode, note, adminUsername string) error {
	op := logging.Start(ctx, s.logger, "AdminService.ReassignTrip", logrus.Fields{
		"trip_id": tripID, "to_de_phone": toDEPhone, "reason_code": reasonCode, "admin": adminUsername,
	})
	defer op.End()

	if !models.IsValidReassignReasonCode(reasonCode) {
		return op.Outcome("invalid_reason", fmt.Errorf("%w: %s", ErrInvalidReasonCode, reasonCode))
	}

	trip, err := s.tripRepo.GetByID(ctx, tripID)
	if err != nil {
		return op.Fail(err)
	}
	if trip == nil {
		return op.Outcome("not_found", fmt.Errorf("%w: %s", ErrTripNotFound, tripID))
	}
	if !isReassignableStatus(trip.Status) {
		return op.Outcome("not_reassignable", fmt.Errorf(
			"%w: status=%s (use POST /admin/assign for pooled trips)", ErrTripNotReassignable, trip.Status))
	}
	if strings.TrimSpace(trip.DEID) == "" || strings.TrimSpace(trip.DEPhone) == "" {
		return op.Outcome("not_reassignable", fmt.Errorf("%w: trip has no current rider", ErrTripNotReassignable))
	}

	toDE, err := s.deRepo.GetByPhone(ctx, toDEPhone)
	if err != nil {
		return op.Fail(err)
	}
	if toDE == nil {
		return op.Outcome("de_not_found", fmt.Errorf("%w: %s", ErrDENotFound, toDEPhone))
	}
	// Must precede every repository call: Reassign puts the outgoing and incoming
	// riders in one transaction, and DynamoDB raises ValidationException if a
	// transaction touches the same item twice. DE items are keyed by phone, so
	// the phone comparison — not the de_id one — is what actually prevents the
	// collision; both are checked because a trip row with a de_id/de_phone pair
	// that disagrees with the DE record should fail closed either way.
	if toDE.DEID == trip.DEID || toDE.PhoneNumber == trip.DEPhone {
		return op.Outcome("same_driver", fmt.Errorf("%w: %s", ErrSameDriver, toDE.DEID))
	}
	if toDE.Status != models.DEStatusEligible && toDE.Status != models.DEStatusFree {
		return op.Outcome("de_not_eligible", fmt.Errorf("%w: driver status=%s", ErrDENotEligible, toDE.Status))
	}
	if toDE.CurrentStoreID != trip.StoreID {
		return op.Outcome("wrong_store", fmt.Errorf(
			"%w: driver on duty at %q, trip store is %q", ErrDriverWrongStore, toDE.CurrentStoreID, trip.StoreID))
	}

	fromStatus := trip.Status
	// Two renderings of one instant, deliberately different — do not "harmonise":
	// the audit entry is read by ops in Zambia local time, while DEStatusEvent.TS
	// must be UTC because it is embedded in the sort key that ListEventsForDay
	// range-scans with UTC day bounds. A local-offset TS sorts outside its day.
	now := timezone.Now()
	eventTS := now.UTC().Format(time.RFC3339)
	entry := models.TripReassignment{
		FromDEID:             trip.DEID,
		FromDEPhone:          trip.DEPhone,
		ToDEID:               toDE.DEID,
		ToDEPhone:            toDE.PhoneNumber,
		TripStatusAtReassign: fromStatus,
		ReasonCode:           reasonCode,
		Note:                 strings.TrimSpace(note),
		AdminUsername:        adminUsername,
		At:                   now.Format(time.RFC3339),
	}

	if err := s.tripRepo.Reassign(ctx, trip.TripID, trip.DEPhone, trip.DEID,
		toDE.DEID, toDE.PhoneNumber, trip.OrderID, trip.StoreID,
		fromStatus == models.TripStatusAssigned, entry); err != nil {
		if errors.Is(err, repository.ErrReassignConflict) {
			return op.Outcome("conflict", fmt.Errorf("%w: %v", ErrReassignConflict, err))
		}
		return op.Fail(err)
	}

	// Detached context: this runs after the transaction has committed. If the
	// admin's browser disconnects right now, ctx is cancelled and fromDE would
	// come back nil — degrading rider B's critical push to "the previous
	// rider" and dropping the one fact (who to collect from) it exists to
	// convey. Best-effort lookups after the commit point use
	// context.Background(), matching afterReassign's own detached sends.
	fromDE, _ := s.deRepo.GetByPhone(context.Background(), trip.DEPhone)
	s.afterReassign(trip, fromDE, toDE, eventTS)
	return nil
}

// afterReassign records presence events and pushes both riders. Best-effort
// throughout: the reassignment has already committed and must not be failed by
// a notification or log write. at must be a UTC RFC3339 stamp (sort-key bound).
func (s *AdminService) afterReassign(
	trip *models.Trip,
	fromDE, toDE *models.DeliveryExecutive,
	at string,
) {
	fromName := "the previous rider"
	fromPhone := trip.DEPhone
	if fromDE != nil {
		if strings.TrimSpace(fromDE.Name) != "" {
			fromName = fromDE.Name
		}
		fromPhone = fromDE.PhoneNumber
	}

	if s.statusEventRepo != nil {
		toState := models.DEStatusBusy
		for _, ev := range []*models.DEStatusEvent{
			{Phone: trip.DEPhone, FromState: models.DEStatusBusy, ToState: models.DEStatusFree,
				Reason: models.ReasonReassigned, StoreID: trip.StoreID, TS: at},
			{Phone: toDE.PhoneNumber, FromState: toDE.Status, ToState: toState,
				Reason: models.ReasonReassigned, StoreID: trip.StoreID, TS: at},
		} {
			if err := s.statusEventRepo.Append(context.Background(), ev); err != nil {
				s.logger.WithError(err).WithField("phone", ev.Phone).
					Warn("reassign: failed to append DE status event")
			}
		}
	}

	if s.notifier == nil {
		return
	}
	// Detached context: cancellation of the request must not kill the sends.
	go func() {
		s.notifier.Send(context.Background(), models.NotificationSendRequest{
			RecipientType: models.RecipientTypeDriver,
			RecipientID:   toDE.DEID,
			EventType:     "ORDER_REASSIGNED",
			Priority:      models.PriorityCritical,
			Title:         "Order reassigned to you",
			Body:          fmt.Sprintf("Collect order from %s — tap for details.", fromName),
			// "type" is injected by buildFCMMessage from EventType; do not repeat it.
			Data: map[string]string{
				"trip_id":       trip.TripID,
				"order_id":      trip.OrderID,
				"from_de_name":  fromName,
				"from_de_phone": fromPhone,
			},
		})
		s.notifier.Send(context.Background(), models.NotificationSendRequest{
			RecipientType: models.RecipientTypeDriver,
			RecipientID:   trip.DEID,
			EventType:     "ORDER_REASSIGNED",
			Priority:      models.PriorityHigh,
			Title:         "Order reassigned",
			Body:          "This order has been moved to another rider. Please wait at your location.",
			Data: map[string]string{
				"trip_id":  trip.TripID,
				"order_id": trip.OrderID,
			},
		})
	}()
}
