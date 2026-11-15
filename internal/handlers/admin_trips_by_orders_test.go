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
	if got.DistanceKM == nil || *got.DistanceKM != 2.4 {
		t.Fatalf("distance_km = %v, want 2.4", got.DistanceKM)
	}
	if got.ReachedAt != "2026-08-26T10:01:00Z" {
		t.Fatalf("reached_at = %s, want 2026-08-26T10:01:00Z", got.ReachedAt)
	}
	if got.TripStatus != string(models.TripStatusCompleted) {
		t.Fatalf("trip_status = %s, want completed", got.TripStatus)
	}
}

func TestTripsByOrders_BatchReturnsEachOrdersDrop(t *testing.T) {
	trip := &models.Trip{
		OrderID:    "ORD-A",
		OrderIDs:   []string{"ORD-A", "ORD-B", "ORD-C"},
		Status:     models.TripStatusCompleted,
		DistanceKM: 11.465,
		CreatedAt:  "2026-11-15T07:32:57Z",
		Tasks: []models.Task{
			{Type: models.TaskTypePickup, OrderID: "ORD-A"},
			{Type: models.TaskTypeDrop, OrderID: "ORD-C", ReachedAt: "2026-11-15T07:37:46Z"},
			{Type: models.TaskTypeDrop, OrderID: "ORD-A", ReachedAt: "2026-11-15T07:43:53Z"},
			{Type: models.TaskTypeDrop, OrderID: "ORD-B", ReachedAt: "2026-11-15T08:01:38Z"},
		},
	}
	solo := func(orderID string, km float64, createdAt string) *models.Trip {
		return &models.Trip{
			OrderID:    orderID,
			OrderIDs:   []string{orderID},
			Status:     models.TripStatusInactive,
			DistanceKM: km,
			CreatedAt:  createdAt,
		}
	}
	stub := &stubTripByOrderLister{
		byOrder: map[string][]*models.Trip{
			"ORD-A": {solo("ORD-A", 2.149, "2026-11-15T07:20:00Z"), trip},
			"ORD-B": {solo("ORD-B", 7.915, "2026-11-15T07:21:00Z"), trip},
			"ORD-C": {solo("ORD-C", 1.396, "2026-11-15T07:22:00Z"), trip},
		},
	}
	h := newTestTripsByOrdersHandlers(stub)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/trips/by-orders?ids=ORD-C,ORD-A,ORD-B", nil)
	rec := httptest.NewRecorder()
	h.GetTripsByOrders(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var resp tripsByOrdersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	if len(resp.Trips) != 3 {
		t.Fatalf("trips len = %d, want 3 body=%s", len(resp.Trips), rec.Body.String())
	}
	wantReached := map[string]string{
		"ORD-C": "2026-11-15T07:37:46Z",
		"ORD-A": "2026-11-15T07:43:53Z",
		"ORD-B": "2026-11-15T08:01:38Z",
	}
	wantKM := map[string]float64{
		"ORD-C": 1.396,
		"ORD-A": 2.149,
		"ORD-B": 7.915,
	}
	for i, got := range resp.Trips {
		wantID := []string{"ORD-C", "ORD-A", "ORD-B"}[i]
		if got.OrderID != wantID {
			t.Fatalf("trips[%d].order_id = %s, want %s", i, got.OrderID, wantID)
		}
		if got.DistanceKM == nil || *got.DistanceKM != wantKM[got.OrderID] {
			t.Fatalf("%s distance_km = %v, want %v (not the tour 11.465)", got.OrderID, got.DistanceKM, wantKM[got.OrderID])
		}
		if got.ReachedAt != wantReached[got.OrderID] {
			t.Fatalf("%s reached_at = %s, want %s", got.OrderID, got.ReachedAt, wantReached[got.OrderID])
		}
		if !got.Batched {
			t.Fatalf("%s batched = false, want true", got.OrderID)
		}
	}
}

func TestTripsByOrders_BatchWithoutSoloOmitsDistance(t *testing.T) {
	trip := &models.Trip{
		OrderID:    "ORD-A",
		OrderIDs:   []string{"ORD-A", "ORD-B"},
		Status:     models.TripStatusCompleted,
		DistanceKM: 11.465,
		CreatedAt:  "2026-11-15T07:32:57Z",
		Tasks: []models.Task{
			{Type: models.TaskTypeDrop, OrderID: "ORD-B", ReachedAt: "2026-11-15T07:37:46Z"},
		},
	}
	stub := &stubTripByOrderLister{byOrder: map[string][]*models.Trip{"ORD-B": {trip}}}
	h := newTestTripsByOrdersHandlers(stub)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/trips/by-orders?ids=ORD-B", nil)
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
		t.Fatalf("trips len = %d, want 1", len(resp.Trips))
	}
	if resp.Trips[0].DistanceKM != nil {
		t.Fatalf("distance_km = %v, want omitted", *resp.Trips[0].DistanceKM)
	}
	if strings.Contains(rec.Body.String(), "distance_km") {
		t.Fatalf("body should omit distance_km, got %s", rec.Body.String())
	}
	if resp.Trips[0].ReachedAt != "2026-11-15T07:37:46Z" {
		t.Fatalf("reached_at = %s", resp.Trips[0].ReachedAt)
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
