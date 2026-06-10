package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

var (
	ErrDENotFound    = errors.New("driver not found")
	ErrDENotEligible = errors.New("driver not eligible")
)

// AdminService force-assigns pooled orders to drivers (ops escape hatch).
// Auth is assumed to be handled upstream of these calls.
type AdminService struct {
	tripRepo *repository.TripRepository
	deRepo   *repository.DERepository
	logger   *logrus.Logger
}

func NewAdminService(tripRepo *repository.TripRepository, deRepo *repository.DERepository, logger *logrus.Logger) *AdminService {
	return &AdminService{tripRepo: tripRepo, deRepo: deRepo, logger: logger}
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
	return nil
}
