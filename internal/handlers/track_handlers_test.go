package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

// --- fakes ---

type fakeOrderFetcher struct {
	raw map[string]json.RawMessage
	err error
}

func (f fakeOrderFetcher) GetOrderRaw(ctx context.Context, orderID string) (map[string]json.RawMessage, error) {
	return f.raw, f.err
}

type trackFakeTripGetter struct {
	trip *models.Trip
	err  error
}

func (f trackFakeTripGetter) GetByOrderID(ctx context.Context, orderID string) (*models.Trip, error) {
	return f.trip, f.err
}

type fakeDEResolver struct {
	de  *models.DeliveryExecutive
	err error
}

func (f fakeDEResolver) GetByPhone(ctx context.Context, phone string) (*models.DeliveryExecutive, error) {
	return f.de, f.err
}

// --- helpers ---

func baseOrder() map[string]json.RawMessage {
	// createdAt 5 minutes ago keeps the order-anchored ETA on-time by default.
	createdAt := timezone.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	return map[string]json.RawMessage{
		"orderNumber":   json.RawMessage(`"ORD1"`),
		"status":        json.RawMessage(`"OUT_FOR_DELIVERY"`),
		"grandTotal":    json.RawMessage(`42.5`),
		"items":         json.RawMessage(`[{"sku":"SKU-1","quantity":2}]`),
		"delivery":      json.RawMessage(`{"address":"12 Cairo Rd","phone":"0971234567"}`),
		"refundSummary": json.RawMessage(`null`),
		"createdAt":     json.RawMessage(`"` + createdAt + `"`),
	}
}

// orderWith returns baseOrder with the status and createdAt overridden.
func orderWith(status, createdAt string) map[string]json.RawMessage {
	o := baseOrder()
	o["status"] = json.RawMessage(`"` + status + `"`)
	if createdAt == "" {
		delete(o, "createdAt")
	} else {
		o["createdAt"] = json.RawMessage(`"` + createdAt + `"`)
	}
	return o
}

func tripWith(status models.TripStatus, dropStatus models.TaskStatus, otp string) *models.Trip {
	return &models.Trip{
		OrderID:   "ORD1",
		Status:    status,
		DEPhone:   "0990000000",
		CreatedAt: timezone.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339),
		Tasks: []models.Task{
			{TaskID: "pick", Type: models.TaskTypePickup, Status: models.TaskStatusCompleted},
			{TaskID: "drop", Type: models.TaskTypeDrop, Status: dropStatus, OTP: otp},
		},
	}
}

func newTrackHandlers(of trackOrderFetcher, tg trackTripGetter, de trackDEResolver) *TrackHandlers {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return &TrackHandlers{tripRepo: tg, deRepo: de, javaClient: of, logger: logger}
}

func doTrack(h *TrackHandlers, orderID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/"+orderID+"/track", nil)
	req = mux.SetURLVars(req, map[string]string{"orderId": orderID})
	rec := httptest.NewRecorder()
	h.Track(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

// --- handler tests ---

func TestTrack_OutForDelivery_ShowsOTPNameETAAndPreservesOrder(t *testing.T) {
	of := fakeOrderFetcher{raw: baseOrder()}
	tg := trackFakeTripGetter{trip: tripWith(models.TripStatusOutForDelivery, models.TaskStatusCreated, "4321")}
	de := fakeDEResolver{de: &models.DeliveryExecutive{Name: "John M."}}
	rec := doTrack(newTrackHandlers(of, tg, de), "ORD1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if string(body["otp"]) != `"4321"` {
		t.Errorf("otp = %s", body["otp"])
	}
	if string(body["de_name"]) != `"John M."` {
		t.Errorf("de_name = %s", body["de_name"])
	}
	if string(body["de_phone"]) != `"0990000000"` {
		t.Errorf("de_phone = %s", body["de_phone"])
	}
	if string(body["eta"]) == "null" || len(body["eta"]) == 0 {
		t.Errorf("eta missing: %s", body["eta"])
	}
	// order fields preserved verbatim
	if string(body["grandTotal"]) != `42.5` || string(body["status"]) != `"OUT_FOR_DELIVERY"` {
		t.Errorf("order fields not preserved: grandTotal=%s status=%s", body["grandTotal"], body["status"])
	}
}

func TestTrack_NoTrip_ETAPresentOTPNameNull(t *testing.T) {
	of := fakeOrderFetcher{raw: baseOrder()}
	tg := trackFakeTripGetter{trip: nil}
	rec := doTrack(newTrackHandlers(of, tg, fakeDEResolver{}), "ORD1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := decodeBody(t, rec)
	// OTP/de_name/de_phone remain trip-derived → null with no trip.
	for _, k := range []string{"otp", "de_name", "de_phone"} {
		if string(body[k]) != "null" {
			t.Errorf("%s = %s, want null", k, body[k])
		}
	}
	// ETA is order-anchored and independent of the trip → present.
	if string(body["eta"]) == "null" || len(body["eta"]) == 0 {
		t.Errorf("eta should be present from order createdAt even without a trip: %s", body["eta"])
	}
}

func TestTrack_FindingDriver_ETAOnlyNoOTPNoName(t *testing.T) {
	of := fakeOrderFetcher{raw: baseOrder()}
	tg := trackFakeTripGetter{trip: tripWith(models.TripStatusAssigned, models.TaskStatusCreated, "4321")}
	rec := doTrack(newTrackHandlers(of, tg, fakeDEResolver{de: &models.DeliveryExecutive{Name: "John M."}}), "ORD1")

	body := decodeBody(t, rec)
	if string(body["otp"]) != "null" {
		t.Errorf("otp should be null before commit: %s", body["otp"])
	}
	if string(body["de_name"]) != "null" {
		t.Errorf("de_name should be null before commit: %s", body["de_name"])
	}
	if string(body["de_phone"]) != "null" {
		t.Errorf("de_phone should be null before commit: %s", body["de_phone"])
	}
	if string(body["eta"]) == "null" {
		t.Error("eta should be present once trip exists")
	}
}

func TestTrack_Delivered_NoOTPNoName(t *testing.T) {
	of := fakeOrderFetcher{raw: baseOrder()}
	tg := trackFakeTripGetter{trip: tripWith(models.TripStatusCompleted, models.TaskStatusCompleted, "4321")}
	rec := doTrack(newTrackHandlers(of, tg, fakeDEResolver{de: &models.DeliveryExecutive{Name: "John M."}}), "ORD1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (delivered must not 400)", rec.Code)
	}
	body := decodeBody(t, rec)
	if string(body["otp"]) != "null" {
		t.Errorf("otp should be null after delivery: %s", body["otp"])
	}
	if string(body["de_name"]) != "null" {
		t.Errorf("de_name should be null after completion: %s", body["de_name"])
	}
	if string(body["de_phone"]) != "null" {
		t.Errorf("de_phone should be null after completion: %s", body["de_phone"])
	}
	if string(body["eta"]) != "null" {
		t.Errorf("eta should be null for a delivered (terminal) trip: %s", body["eta"])
	}
}

func TestTrack_Cancelled_NoOTP(t *testing.T) {
	of := fakeOrderFetcher{raw: baseOrder()}
	tg := trackFakeTripGetter{trip: tripWith(models.TripStatusCancelled, models.TaskStatusCreated, "4321")}
	rec := doTrack(newTrackHandlers(of, tg, fakeDEResolver{}), "ORD1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (cancelled must not 400)", rec.Code)
	}
	body := decodeBody(t, rec)
	if string(body["otp"]) != "null" {
		t.Errorf("otp = %s, want null", body["otp"])
	}
	if string(body["de_name"]) != "null" {
		t.Errorf("de_name should be null for a cancelled trip: %s", body["de_name"])
	}
	if string(body["de_phone"]) != "null" {
		t.Errorf("de_phone should be null for a cancelled trip: %s", body["de_phone"])
	}
	if string(body["eta"]) != "null" {
		t.Errorf("eta should be null for a cancelled (terminal) trip: %s", body["eta"])
	}
}

func TestTrack_Accepted_ShowsOTPNameETA(t *testing.T) {
	of := fakeOrderFetcher{raw: baseOrder()}
	tg := trackFakeTripGetter{trip: tripWith(models.TripStatusAccepted, models.TaskStatusCreated, "4321")}
	de := fakeDEResolver{de: &models.DeliveryExecutive{Name: "John M."}}
	rec := doTrack(newTrackHandlers(of, tg, de), "ORD1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if string(body["otp"]) != `"4321"` {
		t.Errorf("otp = %s", body["otp"])
	}
	if string(body["de_name"]) != `"John M."` {
		t.Errorf("de_name = %s", body["de_name"])
	}
	if string(body["de_phone"]) != `"0990000000"` {
		t.Errorf("de_phone = %s", body["de_phone"])
	}
	if string(body["eta"]) == "null" || len(body["eta"]) == 0 {
		t.Errorf("eta should be present for an accepted (non-terminal) trip: %s", body["eta"])
	}
}

func TestTrack_OrderDelivered_SuppressesETA(t *testing.T) {
	createdAt := timezone.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	of := fakeOrderFetcher{raw: orderWith("DELIVERED", createdAt)}
	tg := trackFakeTripGetter{trip: nil}
	rec := doTrack(newTrackHandlers(of, tg, fakeDEResolver{}), "ORD1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if string(body["eta"]) != "null" {
		t.Errorf("eta should be null for a DELIVERED order: %s", body["eta"])
	}
}

func TestTrack_OrderCancelled_SuppressesETA(t *testing.T) {
	createdAt := timezone.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	of := fakeOrderFetcher{raw: orderWith("CANCELLED", createdAt)}
	tg := trackFakeTripGetter{trip: nil}
	rec := doTrack(newTrackHandlers(of, tg, fakeDEResolver{}), "ORD1")

	body := decodeBody(t, rec)
	if string(body["eta"]) != "null" {
		t.Errorf("eta should be null for a CANCELLED order: %s", body["eta"])
	}
}

func TestTrack_MissingCreatedAt_NullETA(t *testing.T) {
	of := fakeOrderFetcher{raw: orderWith("OUT_FOR_DELIVERY", "")}
	tg := trackFakeTripGetter{trip: tripWith(models.TripStatusOutForDelivery, models.TaskStatusCreated, "4321")}
	rec := doTrack(newTrackHandlers(of, tg, fakeDEResolver{de: &models.DeliveryExecutive{Name: "John M."}}), "ORD1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if string(body["eta"]) != "null" {
		t.Errorf("eta should be null when order createdAt is missing: %s", body["eta"])
	}
	// OTP/de_name/de_phone are unaffected by a missing createdAt.
	if string(body["otp"]) != `"4321"` {
		t.Errorf("otp = %s, want \"4321\"", body["otp"])
	}
	if string(body["de_phone"]) != `"0990000000"` {
		t.Errorf("de_phone = %s, want \"0990000000\"", body["de_phone"])
	}
}

func TestTrack_OrderNotFound_404(t *testing.T) {
	of := fakeOrderFetcher{raw: nil} // 404 maps to nil map
	tg := trackFakeTripGetter{trip: nil}
	rec := doTrack(newTrackHandlers(of, tg, fakeDEResolver{}), "MISSING")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestTrack_OrderServiceError_502(t *testing.T) {
	of := fakeOrderFetcher{err: errors.New("boom")}
	tg := trackFakeTripGetter{trip: nil}
	rec := doTrack(newTrackHandlers(of, tg, fakeDEResolver{}), "ORD1")

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestTrack_TripLookupError_DegradesTo200(t *testing.T) {
	of := fakeOrderFetcher{raw: baseOrder()}
	tg := trackFakeTripGetter{err: errors.New("dynamo down")}
	rec := doTrack(newTrackHandlers(of, tg, fakeDEResolver{}), "ORD1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (trip lookup is best-effort)", rec.Code)
	}
	body := decodeBody(t, rec)
	if string(body["orderNumber"]) != `"ORD1"` {
		t.Errorf("order payload should still be present: %s", body["orderNumber"])
	}
	// Trip-derived fields degrade to null; the order-anchored ETA still shows.
	for _, k := range []string{"otp", "de_name", "de_phone"} {
		if string(body[k]) != "null" {
			t.Errorf("%s = %s, want null on degraded path", k, body[k])
		}
	}
	if string(body["eta"]) == "null" || len(body["eta"]) == 0 {
		t.Errorf("eta should still be present on degraded trip path: %s", body["eta"])
	}
}

// --- computeETA tests (pre-existing, kept here) ---

func TestComputeETA_OnTime(t *testing.T) {
	createdAt := timezone.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	eta := computeETA(createdAt)

	if eta == nil {
		t.Fatal("expected ETA payload, got nil")
	}
	if eta.IsDelayed {
		t.Fatal("expected not delayed at 5 minutes")
	}
	if eta.RemainingMinutes < 9 || eta.RemainingMinutes > 11 {
		t.Fatalf("expected ~10 remaining minutes, got %d", eta.RemainingMinutes)
	}
	if eta.Message != nil {
		t.Fatalf("expected no delay message, got: %s", *eta.Message)
	}
}

func TestComputeETA_Delayed(t *testing.T) {
	createdAt := timezone.Now().Add(-20 * time.Minute).UTC().Format(time.RFC3339)
	eta := computeETA(createdAt)

	if eta == nil {
		t.Fatal("expected ETA payload, got nil")
	}
	if !eta.IsDelayed {
		t.Fatal("expected delayed at 20 minutes")
	}
	if eta.RemainingMinutes != 0 {
		t.Fatalf("expected 0 remaining minutes, got %d", eta.RemainingMinutes)
	}
	if eta.Message == nil {
		t.Fatal("expected delay message")
	}
}

func TestComputeETA_ExactBoundary(t *testing.T) {
	createdAt := timezone.Now().Add(-15 * time.Minute).UTC().Format(time.RFC3339)
	eta := computeETA(createdAt)

	if eta == nil {
		t.Fatal("expected ETA payload")
	}
	if !eta.IsDelayed {
		t.Fatal("expected delayed at exactly 15 minutes")
	}
}

func TestComputeETA_InvalidTimestamp(t *testing.T) {
	eta := computeETA("not-a-timestamp")
	if eta != nil {
		t.Fatal("expected nil for invalid timestamp")
	}
}

func TestEnrichOrderWithTracking_AllPresent(t *testing.T) {
	order := map[string]json.RawMessage{"orderNumber": json.RawMessage(`"ORD1"`)}
	otp := "1234"
	name := "John M."
	phone := "0990000000"
	eta := &ETAPayload{ExpiresAt: "2026-06-25T10:00:00Z", RemainingMinutes: 8, IsDelayed: false}

	if err := enrichOrderWithTracking(order, &otp, &name, &phone, eta); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(order["otp"]) != `"1234"` {
		t.Errorf("otp = %s", order["otp"])
	}
	if string(order["de_name"]) != `"John M."` {
		t.Errorf("de_name = %s", order["de_name"])
	}
	if string(order["de_phone"]) != `"0990000000"` {
		t.Errorf("de_phone = %s", order["de_phone"])
	}
	if string(order["orderNumber"]) != `"ORD1"` {
		t.Errorf("original field clobbered: %s", order["orderNumber"])
	}
	var gotETA ETAPayload
	if err := json.Unmarshal(order["eta"], &gotETA); err != nil {
		t.Fatalf("eta not valid json: %v", err)
	}
	if gotETA.RemainingMinutes != 8 {
		t.Errorf("eta.remaining_minutes = %d", gotETA.RemainingMinutes)
	}
}

func TestEnrichOrderWithTracking_NilsBecomeJSONNull(t *testing.T) {
	order := map[string]json.RawMessage{}
	if err := enrichOrderWithTracking(order, nil, nil, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, k := range []string{"otp", "de_name", "de_phone", "eta"} {
		if string(order[k]) != "null" {
			t.Errorf("%s = %s, want null", k, order[k])
		}
	}
}
