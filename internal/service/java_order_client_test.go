package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirupsen/logrus"
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

func TestJavaOrder_EffectiveOrderID_PrefersOrderNumber(t *testing.T) {
	order := JavaOrder{OrderID: "uuid-123", OrderNumber: "ORD999"}
	if got, want := order.EffectiveOrderID(), "ORD999"; got != want {
		t.Fatalf("EffectiveOrderID() = %q, want %q", got, want)
	}

	order.normalizeOrderID()
	if got, want := order.OrderID, "ORD999"; got != want {
		t.Fatalf("OrderID after normalize = %q, want %q", got, want)
	}
}

func TestJavaOrder_EffectiveOrderID_FallsBackToOrderID(t *testing.T) {
	payload := `{
		"orderId": "uuid-456",
		"orderNumber": null,
		"status": "READY_FOR_DELIVERY",
		"delivery": {"address": "1 Main", "latitude": -15.4, "longitude": 28.3, "phone": "0970000000"},
		"items": []
	}`

	var order JavaOrder
	if err := json.Unmarshal([]byte(payload), &order); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if got, want := order.EffectiveOrderID(), "uuid-456"; got != want {
		t.Fatalf("EffectiveOrderID() = %q, want %q", got, want)
	}
}

func newTestJavaClient(serverURL string) *JavaOrderClient {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return NewJavaOrderClient(serverURL, logger)
}

func TestGetOrderRaw_ReturnsFullPayloadVerbatim(t *testing.T) {
	const body = `{"orderNumber":"ORD123","status":"OUT_FOR_DELIVERY","grandTotal":42.5,"items":[{"sku":"SKU-1","quantity":2}],"delivery":{"address":"12 Cairo Rd","phone":"0971234567"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/order-service/api/v1/orders/ORD123" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	got, err := newTestJavaClient(srv.URL).GetOrderRaw(context.Background(), "ORD123")
	if err != nil {
		t.Fatalf("GetOrderRaw error: %v", err)
	}
	if got == nil {
		t.Fatal("expected payload, got nil")
	}
	if string(got["orderNumber"]) != `"ORD123"` {
		t.Errorf("orderNumber not preserved: %s", got["orderNumber"])
	}
	if string(got["grandTotal"]) != `42.5` {
		t.Errorf("grandTotal not preserved verbatim: %s", got["grandTotal"])
	}
	if _, ok := got["items"]; !ok {
		t.Error("items key missing")
	}
	if _, ok := got["delivery"]; !ok {
		t.Error("delivery key missing")
	}
}

func TestGetOrderRaw_NotFoundReturnsNilNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	got, err := newTestJavaClient(srv.URL).GetOrderRaw(context.Background(), "MISSING")
	if err != nil {
		t.Fatalf("expected nil error for 404, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil map for 404, got %v", got)
	}
}

func TestGetOrderRaw_ServerErrorReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newTestJavaClient(srv.URL).GetOrderRaw(context.Background(), "ORD123")
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
}

// The order-service serializes storeId as a JSON number (it's a Java Long), or
// omits it entirely. Decoding must not fail on the number, and every returned
// order must be stamped with the store the cron queried — since the per-store
// endpoint only ever returns orders for that store.
func TestGetReadyForDeliveryOrders_StampsStoreIDAndToleratesNumericStoreId(t *testing.T) {
	const body = `{
		"content": [
			{"orderNumber": "ORD1037370658", "status": "READY_FOR_DELIVERY", "storeId": 100,
			 "delivery": {"address": "1 Main", "latitude": -15.4, "longitude": 28.3, "phone": "0970000000"},
			 "items": []}
		],
		"meta": {"last": true}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/order-service/api/v1/orders/store/100"; got != want {
			t.Errorf("unexpected path %q, want %q", got, want)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	orders, err := newTestJavaClient(srv.URL).GetReadyForDeliveryOrders(context.Background(), "100")
	if err != nil {
		t.Fatalf("GetReadyForDeliveryOrders error (numeric storeId must not break decode): %v", err)
	}
	if len(orders) != 1 {
		t.Fatalf("expected 1 order, got %d", len(orders))
	}
	if got, want := orders[0].StoreID, "100"; got != want {
		t.Errorf("StoreID not stamped from queried store: got %q, want %q", got, want)
	}
	if got, want := orders[0].EffectiveOrderID(), "ORD1037370658"; got != want {
		t.Errorf("order id = %q, want %q", got, want)
	}
}
