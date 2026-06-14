package service

import (
	"encoding/json"
	"testing"
)

// Guards the wire contract with the Java order-service: the struct tags on
// JavaOrder/JavaOrderItem/JavaDelivery must match the JSON the order-service
// actually returns (OrderResponse.items[] and OrderResponse.delivery). The
// helper-level tests build Go structs directly and so can't catch a misspelled
// tag — this decodes a representative payload instead.
func TestJavaOrder_DecodesItemsAndDeliveryName(t *testing.T) {
	// Mirrors order-service OrderResponse: items[] = {sku, productName,
	// imageUrl, quantity, ...}, delivery = {address, latitude, longitude,
	// phone, notes, name}.
	payload := `{
		"orderId": "ORD-1",
		"status": "READY_FOR_DELIVERY",
		"storeId": "store-1",
		"delivery": {
			"address": "12 Cairo Rd",
			"latitude": -15.41,
			"longitude": 28.28,
			"phone": "0971234567",
			"notes": "gate code 4",
			"name": "Jane Doe"
		},
		"items": [
			{"sku": "SKU-1", "productName": "Milk", "imageUrl": "products/milk/front.jpg", "quantity": 2, "unitPrice": 10.0, "subTotal": 20.0},
			{"sku": "SKU-2", "productName": "Bread", "imageUrl": "products/bread/front.jpg", "quantity": 1, "unitPrice": 8.5, "subTotal": 8.5}
		]
	}`

	var order JavaOrder
	if err := json.Unmarshal([]byte(payload), &order); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if order.Delivery.Name != "Jane Doe" {
		t.Errorf("delivery.name not parsed: got %q", order.Delivery.Name)
	}
	if len(order.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(order.Items))
	}
	first := order.Items[0]
	if first.ProductName != "Milk" || first.ImageURL != "products/milk/front.jpg" ||
		first.Quantity != 2 || first.Sku != "SKU-1" {
		t.Errorf("item[0] tags mismatch: got %+v", first)
	}
}

// Today the order-service delivery payload carries no `name` (that field is a
// deferred upstream change). Decoding must leave Name as the empty string so
// createTrip applies the "Shivang Awasthi" fallback rather than breaking.
func TestJavaOrder_MissingDeliveryNameDecodesEmpty(t *testing.T) {
	payload := `{
		"orderId": "ORD-2",
		"delivery": {"address": "1 Main", "latitude": -15.4, "longitude": 28.3, "phone": "0970000000"},
		"items": []
	}`

	var order JavaOrder
	if err := json.Unmarshal([]byte(payload), &order); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if order.Delivery.Name != "" {
		t.Errorf("expected empty delivery name, got %q", order.Delivery.Name)
	}
}

func TestJavaOrder_EffectiveOrderID_FallsBackToOrderNumber(t *testing.T) {
	payload := `{
		"orderId": null,
		"orderNumber": "ORD1162844363",
		"status": "READY_FOR_DELIVERY",
		"delivery": {"address": "1 Main", "latitude": -15.4, "longitude": 28.3, "phone": "0970000000"},
		"items": []
	}`

	var order JavaOrder
	if err := json.Unmarshal([]byte(payload), &order); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if got, want := order.EffectiveOrderID(), "ORD1162844363"; got != want {
		t.Fatalf("EffectiveOrderID() = %q, want %q", got, want)
	}

	order.normalizeOrderID()
	if got, want := order.OrderID, "ORD1162844363"; got != want {
		t.Fatalf("OrderID after normalize = %q, want %q", got, want)
	}
}

func TestJavaOrder_EffectiveOrderID_PrefersOrderID(t *testing.T) {
	order := JavaOrder{OrderID: "uuid-123", OrderNumber: "ORD999"}
	if got, want := order.EffectiveOrderID(), "uuid-123"; got != want {
		t.Fatalf("EffectiveOrderID() = %q, want %q", got, want)
	}
}
