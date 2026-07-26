package service

import (
	"testing"

	"github.com/qcom/qcom/internal/models"
)

func TestBuildCustomerNotification_OutForDelivery(t *testing.T) {
	trip := &models.Trip{OrderID: "ORD1289752277", CustomerUserID: "US0418437320"}
	de := &models.DeliveryExecutive{Name: "Chanda"}

	req, ok := buildCustomerNotification(trip, de, eventOutForDelivery)
	if !ok {
		t.Fatal("expected notification to be built")
	}
	if req.RecipientType != models.RecipientTypeCustomer {
		t.Fatalf("recipient type = %q, want customer", req.RecipientType)
	}
	if req.RecipientID != "US0418437320" {
		t.Fatalf("recipient id = %q, want US0418437320", req.RecipientID)
	}
	if req.EventType != "ORDER_OUT_FOR_DELIVERY" {
		t.Fatalf("event type = %q", req.EventType)
	}
	if req.Priority != models.PriorityHigh {
		t.Fatalf("priority = %q, want high", req.Priority)
	}
	if req.Title != "On the way!" {
		t.Fatalf("title = %q", req.Title)
	}
	want := "Chanda has picked up order ORD1289752277 and is heading to you."
	if req.Body != want {
		t.Fatalf("body = %q, want %q", req.Body, want)
	}
}

func TestBuildCustomerNotification_Delivered(t *testing.T) {
	trip := &models.Trip{OrderID: "ORD1848863216", CustomerUserID: "d8f8f364-1b3e-4a20-84be-e27245b7c164"}
	de := &models.DeliveryExecutive{Name: "Chanda"}

	req, ok := buildCustomerNotification(trip, de, eventDelivered)
	if !ok {
		t.Fatal("expected notification to be built")
	}
	if req.EventType != "ORDER_DELIVERED" {
		t.Fatalf("event type = %q", req.EventType)
	}
	// UUID-vintage customer IDs must pass through untouched.
	if req.RecipientID != "d8f8f364-1b3e-4a20-84be-e27245b7c164" {
		t.Fatalf("recipient id = %q", req.RecipientID)
	}
	if req.Title != "Delivered!" {
		t.Fatalf("title = %q", req.Title)
	}
	want := "Order ORD1848863216 has been delivered. Thanks for shopping with Bunzo!"
	if req.Body != want {
		t.Fatalf("body = %q, want %q", req.Body, want)
	}
}

func TestBuildCustomerNotification_DeepLinkPayload(t *testing.T) {
	trip := &models.Trip{OrderID: "ORD1289752277", CustomerUserID: "US0418437320"}

	req, ok := buildCustomerNotification(trip, &models.DeliveryExecutive{Name: "Chanda"}, eventOutForDelivery)
	if !ok {
		t.Fatal("expected notification to be built")
	}
	if req.Data["order_number"] != "ORD1289752277" {
		t.Fatalf("order_number = %q", req.Data["order_number"])
	}
	if req.Data["screen"] != "ORDER_DETAILS_SCREEN" {
		t.Fatalf("screen = %q", req.Data["screen"])
	}
	// params must be a JSON *string*, matching the Java ORDER_PACKED contract
	// consumed by BunzoApp notificationNavigation.ts.
	if req.Data["params"] != `{"orderNumber":"ORD1289752277"}` {
		t.Fatalf("params = %q", req.Data["params"])
	}
}

func TestBuildCustomerNotification_BlankDriverNameFallsBack(t *testing.T) {
	trip := &models.Trip{OrderID: "ORD1289752277", CustomerUserID: "US0418437320"}

	req, ok := buildCustomerNotification(trip, &models.DeliveryExecutive{Name: "   "}, eventOutForDelivery)
	if !ok {
		t.Fatal("expected notification to be built")
	}
	want := "Your rider has picked up order ORD1289752277 and is heading to you."
	if req.Body != want {
		t.Fatalf("body = %q, want %q", req.Body, want)
	}
}

func TestBuildCustomerNotification_NilDriverFallsBack(t *testing.T) {
	trip := &models.Trip{OrderID: "ORD1289752277", CustomerUserID: "US0418437320"}

	req, ok := buildCustomerNotification(trip, nil, eventOutForDelivery)
	if !ok {
		t.Fatal("expected notification to be built")
	}
	want := "Your rider has picked up order ORD1289752277 and is heading to you."
	if req.Body != want {
		t.Fatalf("body = %q, want %q", req.Body, want)
	}
}

func TestBuildCustomerNotification_NoCustomerIDSkips(t *testing.T) {
	trip := &models.Trip{OrderID: "ORD1849915231", CustomerUserID: ""}

	if _, ok := buildCustomerNotification(trip, &models.DeliveryExecutive{Name: "Chanda"}, eventDelivered); ok {
		t.Fatal("expected no notification when CustomerUserID is blank")
	}
}

func TestBuildCustomerNotification_NilTripSkips(t *testing.T) {
	if _, ok := buildCustomerNotification(nil, &models.DeliveryExecutive{Name: "Chanda"}, eventDelivered); ok {
		t.Fatal("expected no notification for nil trip")
	}
}
