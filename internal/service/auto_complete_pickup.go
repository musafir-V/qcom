package service

import (
	"context"
)

// AutoCompletePickupIfJavaOFD completes pickup when Java is already
// OUT_FOR_DELIVERY or DELIVERED, or when complete-by-order already recorded
// admin OFD inbound on the trip (Java GET may still look like RFD).
// GetOrderStatus errors are logged and swallowed so assign never fails.
func (s *TripService) AutoCompletePickupIfJavaOFD(ctx context.Context, orderID string) error {
	trigger := false
	if s.javaClient != nil {
		status, err := s.javaClient.GetOrderStatus(ctx, orderID)
		if err != nil {
			s.logger.WithError(err).WithField("order_id", orderID).
				Warn("AutoCompletePickupIfJavaOFD: GetOrderStatus failed")
		} else if status == "OUT_FOR_DELIVERY" || status == "DELIVERED" {
			trigger = true
		}
	}
	if !trigger {
		trip, err := s.tripRepo.GetByOrderID(ctx, orderID)
		if err != nil {
			s.logger.WithError(err).WithField("order_id", orderID).
				Warn("AutoCompletePickupIfJavaOFD: trip lookup failed")
			return nil
		}
		if trip == nil || !trip.AdminOFDInbound {
			return nil
		}
	}
	_, err := s.CompleteByOrder(ctx, CompleteByOrderInput{
		OrderID: orderID,
		Status:  "OUT_FOR_DELIVERY",
	})
	return err
}
