package service

import (
	"fmt"
	"strings"

	"github.com/qcom/qcom/internal/models"
)

// customerOrderEvent identifies a customer-facing order push triggered by
// driver action. The string value is the FCM event_type sent to the app.
type customerOrderEvent string

const (
	eventOutForDelivery customerOrderEvent = "ORDER_OUT_FOR_DELIVERY"
	eventDelivered      customerOrderEvent = "ORDER_DELIVERED"
)

// orderDetailsScreen is the BunzoApp route name read by
// notificationNavigation.ts. It must stay in sync with
// NAVIGATION_CONSTANTS.ORDER_DETAILS_SCREEN and with the Java
// QcomNotificationClient.ORDER_DETAILS_SCREEN constant used by ORDER_PACKED.
const orderDetailsScreen = "ORDER_DETAILS_SCREEN"

// driverNameFallback is used when the DE record carries no usable name.
const driverNameFallback = "Your rider"

// buildCustomerNotification renders the push for a driver-triggered customer
// event. It is pure: no I/O, no clock, no randomness — so it is fully
// unit-testable, which the previous inline implementation was not.
//
// Returns ok=false when the trip carries no customer to notify. Trips created
// before Trip.CustomerUserID existed fall into this case; the caller logs and
// moves on rather than failing the driver's request.
func buildCustomerNotification(
	trip *models.Trip,
	de *models.DeliveryExecutive,
	event customerOrderEvent,
) (models.NotificationSendRequest, bool) {
	if trip == nil || strings.TrimSpace(trip.CustomerUserID) == "" {
		return models.NotificationSendRequest{}, false
	}

	// Trip.OrderID is already the human-readable order number: the assignment
	// cron stores JavaOrder.EffectiveOrderID(), which prefers orderNumber over
	// the raw UUID. This is exactly what OrderDetailsScreen passes to
	// trackOrder(), so no further resolution is needed.
	orderNumber := trip.OrderID

	data := orderNavigationData(orderNumber)

	var title, body string
	switch event {
	case eventOutForDelivery:
		title = "On the way!"
		body = fmt.Sprintf("%s has picked up order %s and is heading to you.", driverName(de), orderNumber)
		// The OTP the customer must read out to the rider. It rides along in the
		// push so they have it without opening the app; the tracking endpoint
		// remains the source of truth. Older trips may carry no OTP, in which
		// case the push degrades to the plain heading-to-you copy.
		if otp := deliveryOTP(trip); otp != "" {
			body += fmt.Sprintf(" Share OTP %s to receive it.", otp)
			data["delivery_otp"] = otp
		}
	case eventDelivered:
		title = "Delivered!"
		body = fmt.Sprintf("Order %s has been delivered. Thanks for shopping with Bunzo!", orderNumber)
	default:
		return models.NotificationSendRequest{}, false
	}

	return models.NotificationSendRequest{
		RecipientType: models.RecipientTypeCustomer,
		RecipientID:   trip.CustomerUserID,
		EventType:     string(event),
		Priority:      models.PriorityHigh,
		Title:         title,
		Body:          body,
		Data:          data,
	}, true
}

// deliveryOTP returns the drop task's OTP, or "" when the trip has no drop task
// or the task carries no usable OTP.
func deliveryOTP(trip *models.Trip) string {
	drop := trip.DropTask()
	if drop == nil {
		return ""
	}
	return strings.TrimSpace(drop.OTP)
}

// driverName returns the DE's display name, or a neutral fallback when the
// record has no usable name.
func driverName(de *models.DeliveryExecutive) string {
	if de == nil {
		return driverNameFallback
	}
	if name := strings.TrimSpace(de.Name); name != "" {
		return name
	}
	return driverNameFallback
}

// orderNavigationData builds the deep-link payload BunzoApp expects.
// notificationNavigation.ts reads "screen" and "params" only; "params" must be
// a JSON *string* because FCM data values are strings. Kept byte-identical to
// the Java ORDER_PACKED payload so all customer order pushes deep-link alike.
func orderNavigationData(orderNumber string) map[string]string {
	return map[string]string{
		"order_number": orderNumber,
		"screen":       orderDetailsScreen,
		"params":       fmt.Sprintf(`{"orderNumber":%q}`, orderNumber),
	}
}
