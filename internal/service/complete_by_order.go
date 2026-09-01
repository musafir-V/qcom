package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

// ErrInvalidStatus is returned when CompleteByOrder is called with a status
// other than OUT_FOR_DELIVERY or DELIVERED.
var ErrInvalidStatus = errors.New("invalid status")

// CompleteByOrderInput is the Java admin OFD/DELIVERED inbound for an order.
type CompleteByOrderInput struct {
	OrderID string
	Status  string
}

// CompleteByOrder applies admin OFD (pickup complete) or DELIVERED (pickup+drop
// + free rider) using the same applyTaskCompletion path as the rider, with
// skipJava so the packed gate never 409s after admin already moved Java.
func (s *TripService) CompleteByOrder(ctx context.Context, in CompleteByOrderInput) (PaymentUpdateResult, error) {
	op := logging.Start(ctx, s.logger, "TripService.CompleteByOrder", logrus.Fields{
		"order_id": in.OrderID, "status": in.Status,
	})
	defer op.End()

	switch in.Status {
	case "OUT_FOR_DELIVERY", "DELIVERED":
	default:
		return PaymentUpdateResult{}, op.Outcome("invalid_status", fmt.Errorf("%w: %s", ErrInvalidStatus, in.Status))
	}

	trip, err := s.tripRepo.GetByOrderID(ctx, in.OrderID)
	if err != nil {
		return PaymentUpdateResult{}, op.Fail(err)
	}
	if trip == nil {
		op.Outcome("no_active_trip", nil)
		return PaymentUpdateResult{Updated: false, Reason: "no_active_trip"}, nil
	}
	if trip.Status == models.TripStatusCompleted || trip.Status == models.TripStatusCancelled ||
		trip.Status == models.TripStatusDistanceFailed {
		op.Outcome("trip_terminal", nil)
		return PaymentUpdateResult{Updated: false, Reason: "trip_terminal"}, nil
	}

	switch in.Status {
	case "OUT_FOR_DELIVERY":
		return s.completeOFDInbound(ctx, trip)
	case "DELIVERED":
		return s.completeDeliveredInbound(ctx, trip)
	default:
		return PaymentUpdateResult{}, op.Outcome("invalid_status", fmt.Errorf("%w: %s", ErrInvalidStatus, in.Status))
	}
}

// riderOnTrip reports whether a DE is assigned to the trip (DEID nonempty).
func riderOnTrip(trip *models.Trip) bool {
	return trip != nil && trip.DEID != ""
}

// AutoCompletePickupIfJavaOFD is a T1 stub; T5 implements the Java-status check.
func (s *TripService) AutoCompletePickupIfJavaOFD(ctx context.Context, orderID string) error {
	return nil
}

func (s *TripService) completeDeliveredInbound(ctx context.Context, trip *models.Trip) (PaymentUpdateResult, error) {
	_ = ctx
	_ = trip
	return PaymentUpdateResult{}, nil
}
