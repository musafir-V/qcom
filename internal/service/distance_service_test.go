package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirupsen/logrus"
)

func newTestDistanceService(t *testing.T, body string, statusCode int) *DistanceService {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(statusCode)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	logger := logrus.New()
	logger.SetOutput(io.Discard)
	s := NewDistanceService("test-key", logger)
	s.baseURL = srv.URL
	return s
}

func TestDistanceKM_OK_ReturnsKilometres(t *testing.T) {
	body := `{"status":"OK","rows":[{"elements":[{"status":"OK","distance":{"value":4200}}]}]}`
	s := newTestDistanceService(t, body, http.StatusOK)

	km, err := s.DistanceKM(context.Background(), -15.40, 28.26, -15.41, 28.30)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if km != 4.2 {
		t.Fatalf("expected 4.2 km, got %v", km)
	}
}

func TestDistanceKM_ElementZeroResults_IsErrNoRoute(t *testing.T) {
	body := `{"status":"OK","rows":[{"elements":[{"status":"ZERO_RESULTS"}]}]}`
	s := newTestDistanceService(t, body, http.StatusOK)

	_, err := s.DistanceKM(context.Background(), 12.97, 77.64, 37.46, -121.90)
	if !errors.Is(err, ErrNoRoute) {
		t.Fatalf("expected ErrNoRoute for element ZERO_RESULTS, got %v", err)
	}
}

func TestDistanceKM_ElementNotFound_IsErrNoRoute(t *testing.T) {
	body := `{"status":"OK","rows":[{"elements":[{"status":"NOT_FOUND"}]}]}`
	s := newTestDistanceService(t, body, http.StatusOK)

	_, err := s.DistanceKM(context.Background(), 12.97, 77.64, 0, 0)
	if !errors.Is(err, ErrNoRoute) {
		t.Fatalf("expected ErrNoRoute for element NOT_FOUND, got %v", err)
	}
}

func TestDistanceKM_TopLevelNotFound_IsErrNoRoute(t *testing.T) {
	body := `{"status":"NOT_FOUND","rows":[]}`
	s := newTestDistanceService(t, body, http.StatusOK)

	_, err := s.DistanceKM(context.Background(), 12.97, 77.64, 0, 0)
	if !errors.Is(err, ErrNoRoute) {
		t.Fatalf("expected ErrNoRoute for top-level NOT_FOUND, got %v", err)
	}
}

func TestDistanceKM_OverQueryLimit_IsNotErrNoRoute(t *testing.T) {
	// Transient statuses must stay retryable (NOT ErrNoRoute) so the caller keeps
	// trying (subject to backoff) rather than terminally failing the order.
	body := `{"status":"OVER_QUERY_LIMIT","rows":[]}`
	s := newTestDistanceService(t, body, http.StatusOK)

	_, err := s.DistanceKM(context.Background(), 12.97, 77.64, 12.98, 77.65)
	if err == nil {
		t.Fatal("expected an error for OVER_QUERY_LIMIT")
	}
	if errors.Is(err, ErrNoRoute) {
		t.Fatalf("OVER_QUERY_LIMIT must not be classified as ErrNoRoute, got %v", err)
	}
}

func TestIsNoRouteStatus(t *testing.T) {
	noRoute := []string{"ZERO_RESULTS", "NOT_FOUND"}
	for _, s := range noRoute {
		if !isNoRouteStatus(s) {
			t.Errorf("expected %q to be a no-route status", s)
		}
	}
	retryable := []string{"OK", "OVER_QUERY_LIMIT", "UNKNOWN_ERROR", "REQUEST_DENIED", "INVALID_REQUEST", ""}
	for _, s := range retryable {
		if isNoRouteStatus(s) {
			t.Errorf("expected %q to NOT be a no-route status", s)
		}
	}
}
