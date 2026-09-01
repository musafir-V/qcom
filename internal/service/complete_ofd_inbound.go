package service

import (
	"context"
	"fmt"

	"github.com/qcom/qcom/internal/models"
)

// completeOFDInbound completes pickup when admin marks Java OUT_FOR_DELIVERY.
// No requirePacked (admin already moved Java). Does not call AdminCompleteTask.
// skipJava=true so Java is never written from this inbound.
func (s *TripService) markAdminOFDInbound(ctx context.Context, trip *models.Trip) error {
	if trip.AdminOFDInbound {
		return nil
	}
	if err := s.tripRepo.MarkAdminOFDInbound(ctx, trip.TripID); err != nil {
		return err
	}
	trip.AdminOFDInbound = true
	return nil
}

func (s *TripService) completeOFDInbound(ctx context.Context, trip *models.Trip) (PaymentUpdateResult, error) {
	if !riderOnTrip(trip) {
		if err := s.markAdminOFDInbound(ctx, trip); err != nil {
			return PaymentUpdateResult{}, err
		}
		return PaymentUpdateResult{Updated: false, Reason: "no_rider"}, nil
	}

	pickup := trip.PickupTask()
	if pickup == nil {
		return PaymentUpdateResult{}, fmt.Errorf("%w: pickup task missing", ErrTaskNotFound)
	}
	if pickup.Status == models.TaskStatusCompleted {
		return PaymentUpdateResult{Updated: false, Reason: "already_done"}, nil
	}

	de, err := s.deRepo.GetByPhone(ctx, trip.DEPhone)
	if err != nil {
		return PaymentUpdateResult{}, err
	}
	if de == nil {
		return PaymentUpdateResult{}, fmt.Errorf("%w: %s", ErrDENotFound, trip.DEPhone)
	}

	if trip.Status != models.TripStatusAccepted {
		if err := s.tripRepo.Accept(ctx, trip.TripID, de.DEID); err != nil {
			return PaymentUpdateResult{}, err
		}
		trip.Status = models.TripStatusAccepted
	}
	if err := validateTaskTransition(*pickup, models.TaskStatusCompleted, false); err != nil {
		return PaymentUpdateResult{}, err
	}
	if err := s.applyTaskCompletion(ctx, trip, pickup, de, models.TaskStatusCompleted, "", "dashboard", true); err != nil {
		return PaymentUpdateResult{}, err
	}
	if err := s.markAdminOFDInbound(ctx, trip); err != nil {
		return PaymentUpdateResult{}, err
	}
	return PaymentUpdateResult{Updated: true}, nil
}
