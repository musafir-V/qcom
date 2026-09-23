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
	return newTestDistanceServiceInspect(t, body, statusCode, nil)
}

func newTestDistanceServiceInspect(t *testing.T, body string, statusCode int, inspect func(*http.Request)) *DistanceService {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if inspect != nil {
			inspect(r)
		}
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

func TestDistanceKM_SendsDrivingMode(t *testing.T) {
	body := `{"status":"OK","rows":[{"elements":[{"status":"OK","distance":{"value":1000}}]}]}`
	var mode string
	s := newTestDistanceServiceInspect(t, body, http.StatusOK, func(r *http.Request) {
		mode = r.URL.Query().Get("mode")
	})

	if _, err := s.DistanceKM(context.Background(), -15.40, 28.26, -15.41, 28.30); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != "driving" {
		t.Fatalf("mode = %q, want driving", mode)
	}
}

func TestCompute_HaversineDoesNotCallGoogle(t *testing.T) {
	called := false
	s := newTestDistanceServiceInspect(t, `{}`, http.StatusOK, func(*http.Request) {
		called = true
	})

	out, err := s.Compute(context.Background(), -15.40, 28.26, -15.41, 28.30, DistanceMethodHaversine)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("haversine must not call Distance Matrix")
	}
	if out.Method != DistanceMethodHaversine {
		t.Fatalf("method = %q", out.Method)
	}
	if out.HaversineKM == nil || *out.HaversineKM <= 0 {
		t.Fatalf("haversine_km = %v, want > 0", out.HaversineKM)
	}
	if out.GoogleKM != nil {
		t.Fatalf("google_km = %v, want nil", out.GoogleKM)
	}
}

func TestCompute_GoogleOnly(t *testing.T) {
	body := `{"status":"OK","rows":[{"elements":[{"status":"OK","distance":{"value":4200}}]}]}`
	s := newTestDistanceService(t, body, http.StatusOK)

	out, err := s.Compute(context.Background(), -15.40, 28.26, -15.41, 28.30, DistanceMethodGoogle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.HaversineKM != nil {
		t.Fatalf("haversine_km = %v, want nil", out.HaversineKM)
	}
	if out.GoogleKM == nil || *out.GoogleKM != 4.2 {
		t.Fatalf("google_km = %v, want 4.2", out.GoogleKM)
	}
}

func TestCompute_GoogleNoRoute_ReturnsErrNoRoute(t *testing.T) {
	body := `{"status":"OK","rows":[{"elements":[{"status":"ZERO_RESULTS"}]}]}`
	s := newTestDistanceService(t, body, http.StatusOK)

	_, err := s.Compute(context.Background(), 12.97, 77.64, 37.46, -121.90, DistanceMethodGoogle)
	if !errors.Is(err, ErrNoRoute) {
		t.Fatalf("expected ErrNoRoute, got %v", err)
	}
}

func TestCompute_BothPartialOnNoRoute(t *testing.T) {
	body := `{"status":"OK","rows":[{"elements":[{"status":"ZERO_RESULTS"}]}]}`
	s := newTestDistanceService(t, body, http.StatusOK)

	out, err := s.Compute(context.Background(), -15.40, 28.26, -15.41, 28.30, "")
	if err != nil {
		t.Fatalf("both must not fail on Google NO_ROUTE: %v", err)
	}
	if out.Method != DistanceMethodBoth {
		t.Fatalf("method = %q, want both", out.Method)
	}
	if out.HaversineKM == nil {
		t.Fatal("haversine_km must be set")
	}
	if out.GoogleKM != nil {
		t.Fatalf("google_km = %v, want nil", out.GoogleKM)
	}
	if out.GoogleError == nil || *out.GoogleError != GoogleErrorNoRoute {
		t.Fatalf("google_error = %v, want NO_ROUTE", out.GoogleError)
	}
}

func TestCompute_BothPartialOnUpstream(t *testing.T) {
	body := `{"status":"OVER_QUERY_LIMIT","rows":[]}`
	s := newTestDistanceService(t, body, http.StatusOK)

	out, err := s.Compute(context.Background(), -15.40, 28.26, -15.41, 28.30, DistanceMethodBoth)
	if err != nil {
		t.Fatalf("both must not fail on Google upstream: %v", err)
	}
	if out.GoogleError == nil || *out.GoogleError != GoogleErrorUpstream {
		t.Fatalf("google_error = %v, want UPSTREAM", out.GoogleError)
	}
}

func TestCompute_InvalidMethod(t *testing.T) {
	s := newTestDistanceService(t, `{}`, http.StatusOK)
	_, err := s.Compute(context.Background(), -15.40, 28.26, -15.41, 28.30, "walking")
	if !errors.Is(err, ErrInvalidDistanceMethod) {
		t.Fatalf("expected ErrInvalidDistanceMethod, got %v", err)
	}
}

func TestNormalizeDistanceMethod(t *testing.T) {
	got, err := NormalizeDistanceMethod("  GOOGLE ")
	if err != nil || got != DistanceMethodGoogle {
		t.Fatalf("got %q %v, want google", got, err)
	}
	got, err = NormalizeDistanceMethod("")
	if err != nil || got != DistanceMethodBoth {
		t.Fatalf("empty default = %q %v, want both", got, err)
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
