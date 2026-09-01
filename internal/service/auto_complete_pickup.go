package service

import (
	"context"
)

// AutoCompletePickupIfJavaOFD completes pickup when Java is already
// OUT_FOR_DELIVERY or DELIVERED (admin packed/OFD before or at assign).
// GetOrderStatus errors are logged and swallowed so assign never fails.
func (s *TripService) AutoCompletePickupIfJavaOFD(ctx context.Context, orderID string) error {
	if s.javaClient == nil {
		return nil
	}
	status, err := s.javaClient.GetOrderStatus(ctx, orderID)
	if err != nil {
		s.logger.WithError(err).WithField("order_id", orderID).
			Warn("AutoCompletePickupIfJavaOFD: GetOrderStatus failed")
		return nil
	}
	if status != "OUT_FOR_DELIVERY" && status != "DELIVERED" {
		return nil
	}
	_, err = s.CompleteByOrder(ctx, CompleteByOrderInput{
		OrderID: orderID,
		Status:  "OUT_FOR_DELIVERY",
	})
	return err
}
