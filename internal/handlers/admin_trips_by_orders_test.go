package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type stubTripByOrderLister struct {
	byOrder map[string][]*models.Trip
	err     error
	calls   []string
}

func (s *stubTripByOrderLister) ListByOrderID(_ context.Context, orderID string) ([]*models.Trip, error) {
	s.calls = append(s.calls, orderID)
	if s.err != nil {
		return nil, s.err
	}
	if s.byOrder == nil {
		return nil, nil
	}
	return s.byOrder[orderID], nil
}

func newTestTripsByOrdersHandlers(lister tripByOrderLister) *AdminTripsByOrdersHandlers {
	logger := logrus.New()
	logger.SetOutput(logrus.New().Out)
	return newAdminTripsByOrdersHandlers(lister, logger)
}

func decodeTripsByOrdersError(t *testing.T, rec *httptest.ResponseRecorder) ErrorResponse {
	t.Helper()
	var env ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v body=%s", err, rec.Body.String())
	}
	return env
}

func TestTripsByOrders_MissingIds(t *testing.T) {
	h := newTestTripsByOrdersHandlers(&stubTripByOrderLister{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/trips/by-orders", nil)
	rec := httptest.NewRecorder()
	h.GetTripsByOrders(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	env := decodeTripsByOrdersError(t, rec)
	if env.Error.Code != "MISSING_PARAM" {
		t.Fatalf("code = %s, want MISSING_PARAM", env.Error.Code)
	}
}

func TestTripsByOrders_BlankIds(t *testing.T) {
	h := newTestTripsByOrdersHandlers(&stubTripByOrderLister{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/trips/by-orders?ids=", nil)
	rec := httptest.NewRecorder()
	h.GetTripsByOrders(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	env := decodeTripsByOrdersError(t, rec)
	if env.Error.Code != "MISSING_PARAM" {
		t.Fatalf("code = %s, want MISSING_PARAM", env.Error.Code)
	}
}

func TestTripsByOrders_TooManyIds(t *testing.T) {
	ids := make([]string, 101)
	for i := range ids {
		ids[i] = fmt.Sprintf("ORD-%d", i)
	}
	h := newTestTripsByOrdersHandlers(&stubTripByOrderLister{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/trips/by-orders?ids="+strings.Join(ids, ","), nil)
	rec := httptest.NewRecorder()
	h.GetTripsByOrders(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	env := decodeTripsByOrdersError(t, rec)
	if env.Error.Code != "TOO_MANY_IDS" {
		t.Fatalf("code = %s, want TOO_MANY_IDS", env.Error.Code)
	}
}

func TestTripsByOrders_TwoOrdersPrefersCompleted(t *testing.T) {
	completed := &models.Trip{
		OrderID:    "ORD-A",
		Status:     models.TripStatusCompleted,
		DistanceKM: 2.4,
		CreatedAt:  "2026-08-26T08:00:00Z",
		Tasks: []models.Task{
			{Type: models.TaskTypeDrop, ReachedAt: "2026-08-26T10:01:00Z"},
		},
	}
	cancelled := &models.Trip{
		OrderID:    "ORD-A",
		Status:     models.TripStatusCancelled,
		DistanceKM: 9,
		CreatedAt:  "2026-08-26T09:00:00Z",
	}
	stub := &stubTripByOrderLister{
		byOrder: map[string][]*models.Trip{
			"ORD-A": {cancelled, completed},
		},
	}
	h := newTestTripsByOrdersHandlers(stub)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/trips/by-orders?ids=ORD-A,ORD-B", nil)
	rec := httptest.NewRecorder()
	h.GetTripsByOrders(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var resp tripsByOrdersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	if len(resp.Trips) != 1 {
		t.Fatalf("trips len = %d, want 1 body=%s", len(resp.Trips), rec.Body.String())
	}
	got := resp.Trips[0]
	if got.OrderID != "ORD-A" {
		t.Fatalf("order_id = %s, want ORD-A", got.OrderID)
	}
	if got.DistanceKM != 2.4 {
		t.Fatalf("distance_km = %v, want 2.4", got.DistanceKM)
	}
	if got.ReachedAt != "2026-08-26T10:01:00Z" {
		t.Fatalf("reached_at = %s, want 2026-08-26T10:01:00Z", got.ReachedAt)
	}
	if got.TripStatus != string(models.TripStatusCompleted) {
		t.Fatalf("trip_status = %s, want completed", got.TripStatus)
	}
}

func TestTripsByOrders_DeduplicatesIds(t *testing.T) {
	completed := &models.Trip{
		OrderID:    "ORD-A",
		Status:     models.TripStatusCompleted,
		DistanceKM: 1,
		Tasks: []models.Task{
			{Type: models.TaskTypeDrop, ReachedAt: "2026-08-26T10:01:00Z"},
		},
	}
	stub := &stubTripByOrderLister{
		byOrder: map[string][]*models.Trip{
			"ORD-A": {completed},
		},
	}
	h := newTestTripsByOrdersHandlers(stub)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/trips/by-orders?ids=ORD-A,ORD-A", nil)
	rec := httptest.NewRecorder()
	h.GetTripsByOrders(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(stub.calls) != 1 || stub.calls[0] != "ORD-A" {
		t.Fatalf("ListByOrderID calls = %v, want [ORD-A]", stub.calls)
	}
}

func TestTripsByOrders_EmptyMatch(t *testing.T) {
	h := newTestTripsByOrdersHandlers(&stubTripByOrderLister{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/trips/by-orders?ids=ORD-MISSING", nil)
	rec := httptest.NewRecorder()
	h.GetTripsByOrders(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != `{"trips":[]}` && !strings.Contains(rec.Body.String(), `"trips":[]`) {
		t.Fatalf("body = %s, want {\"trips\":[]}", rec.Body.String())
	}
	var resp tripsByOrdersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Trips == nil {
		t.Fatal("trips must be [] not null")
	}
	if len(resp.Trips) != 0 {
		t.Fatalf("trips len = %d, want 0", len(resp.Trips))
	}
}

func TestTripsByOrders_ListerError(t *testing.T) {
	h := newTestTripsByOrdersHandlers(&stubTripByOrderLister{err: errors.New("ddb down")})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/trips/by-orders?ids=ORD-A", nil)
	rec := httptest.NewRecorder()
	h.GetTripsByOrders(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d (%s)", rec.Code, rec.Body.String())
	}
	env := decodeTripsByOrdersError(t, rec)
	if env.Error.Code != "TRIPS_FETCH_FAILED" {
		t.Fatalf("code = %s, want TRIPS_FETCH_FAILED", env.Error.Code)
	}
}
