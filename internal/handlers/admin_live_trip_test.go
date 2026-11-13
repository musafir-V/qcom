package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type stubLiveTrip struct {
	trip *models.Trip
	err  error
	got  string
}

func (s *stubLiveTrip) LiveTripByOrderID(_ context.Context, orderID string) (*models.Trip, error) {
	s.got = orderID
	if s.err != nil {
		return nil, s.err
	}
	return s.trip, nil
}

func newTestLiveTripHandlers(live liveTripGetter) *AdminTripsByOrdersHandlers {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return &AdminTripsByOrdersHandlers{live: live, logger: logger}
}

func TestGetLiveTrip_Created(t *testing.T) {
	stub := &stubLiveTrip{trip: &models.Trip{
		TripID:  "t1",
		OrderID: "ORD1",
		Status:  models.TripStatusCreated,
		StoreID: "221",
	}}
	h := newTestLiveTripHandlers(stub)
	req := httptest.NewRequest(http.MethodGet, "/admin/orders/ORD1/live-trip", nil)
	req = mux.SetURLVars(req, map[string]string{"orderId": "ORD1"})
	rec := httptest.NewRecorder()

	h.GetLiveTrip(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body %s", rec.Code, rec.Body.String())
	}
	var got liveTripResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.OrderID != "ORD1" || got.TripID != "t1" || got.Status != "created" || got.StoreID != "221" {
		t.Fatalf("body: %+v", got)
	}
	if stub.got != "ORD1" {
		t.Fatalf("lookup id: got %q", stub.got)
	}
}

func TestGetLiveTrip_NoLiveTrip(t *testing.T) {
	h := newTestLiveTripHandlers(&stubLiveTrip{})
	req := httptest.NewRequest(http.MethodGet, "/admin/orders/ORD1/live-trip", nil)
	req = mux.SetURLVars(req, map[string]string{"orderId": "ORD1"})
	rec := httptest.NewRecorder()

	h.GetLiveTrip(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
	var env ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.Code != "NO_LIVE_TRIP" {
		t.Fatalf("code: got %q", env.Error.Code)
	}
}

func TestGetLiveTrip_MissingOrderID(t *testing.T) {
	h := newTestLiveTripHandlers(&stubLiveTrip{})
	req := httptest.NewRequest(http.MethodGet, "/admin/orders//live-trip", nil)
	req = mux.SetURLVars(req, map[string]string{"orderId": "  "})
	rec := httptest.NewRecorder()

	h.GetLiveTrip(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
	var env ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.Code != "MISSING_PARAM" {
		t.Fatalf("code: got %q", env.Error.Code)
	}
}

func TestGetLiveTrip_LookupError(t *testing.T) {
	h := newTestLiveTripHandlers(&stubLiveTrip{err: errors.New("ddb down")})
	req := httptest.NewRequest(http.MethodGet, "/admin/orders/ORD1/live-trip", nil)
	req = mux.SetURLVars(req, map[string]string{"orderId": "ORD1"})
	rec := httptest.NewRecorder()

	h.GetLiveTrip(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500", rec.Code)
	}
	var env ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error.Code != "LIVE_TRIP_FETCH_FAILED" {
		t.Fatalf("code: got %q", env.Error.Code)
	}
}
