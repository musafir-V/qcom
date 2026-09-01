package service

import (
	"context"
	"fmt"
	"time"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/timezone"
)

// completeDeliveredInbound closes the trip when admin marks Java DELIVERED.
// With a rider: completePickupThenDrop(..., "dashboard", true) so Java is never
// force-delivered from this inbound. Without a rider: synthesize pickup/drop
// completed and CompleteTripOnly (nobody to free).
func (s *TripService) completeDeliveredInbound(ctx context.Context, trip *models.Trip) (PaymentUpdateResult, error) {
	if riderOnTrip(trip) {
		de, err := s.deRepo.GetByPhone(ctx, trip.DEPhone)
		if err != nil {
			return PaymentUpdateResult{}, err
		}
		if de == nil {
			return PaymentUpdateResult{}, fmt.Errorf("%w: %s", ErrDENotFound, trip.DEPhone)
		}
		if err := s.completePickupThenDrop(ctx, trip, de, "dashboard", true); err != nil {
			return PaymentUpdateResult{}, err
		}
		return PaymentUpdateResult{Updated: true}, nil
	}

	now := timezone.Now().Format(time.RFC3339)
	for i := range trip.Tasks {
		task := &trip.Tasks[i]
		if task.Type != models.TaskTypePickup && task.Type != models.TaskTypeDrop {
			continue
		}
		if task.Status != models.TaskStatusCompleted {
			if task.Type == models.TaskTypeDrop {
				synthesizeAdminDropReached(task)
			}
			task.Status = models.TaskStatusCompleted
			if task.CompletedAt == "" {
				task.CompletedAt = now
			}
		}
	}
	if err := s.tripRepo.CompleteTripOnly(ctx, trip.TripID, trip.Tasks); err != nil {
		return PaymentUpdateResult{}, err
	}
	return PaymentUpdateResult{Updated: true}, nil
}
