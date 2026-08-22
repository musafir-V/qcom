package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type stubTripReachedConfigStore struct {
	cfg    *models.TripReachedConfig
	getErr error
	putErr error
	putTo  *models.TripReachedConfig
}

func (s *stubTripReachedConfigStore) Get(_ context.Context) (*models.TripReachedConfig, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.cfg == nil {
		return &models.TripReachedConfig{}, nil
	}
	return s.cfg, nil
}

func (s *stubTripReachedConfigStore) Put(_ context.Context, cfg *models.TripReachedConfig) error {
	if s.putErr != nil {
		return s.putErr
	}
	s.putTo = cfg
	s.cfg = cfg
	return nil
}

func newTestTripReachedHandlers(repo tripReachedConfigStore) *AdminTripReachedHandlers {
	logger := logrus.New()
	logger.SetOutput(logrus.New().Out)
	return newAdminTripReachedHandlers(repo, logger)
}

func TestGetTripReached_Default(t *testing.T) {
	h := newTestTripReachedHandlers(&stubTripReachedConfigStore{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/config/drop-reached", nil)
	rec := httptest.NewRecorder()
	h.GetConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var resp tripReachedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.RadiusMeters != models.DefaultReachedRadiusMeters {
		t.Fatalf("expected radius_meters=%v, got %v", models.DefaultReachedRadiusMeters, resp.RadiusMeters)
	}
	if resp.RequireReachedBeforeComplete {
		t.Fatal("expected require_reached_before_complete=false by default")
	}
}

func TestPatchTripReached_Success(t *testing.T) {
	stub := &stubTripReachedConfigStore{}
	h := newTestTripReachedHandlers(stub)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/config/drop-reached", strings.NewReader(`{"radius_meters":200,"require_reached_before_complete":true}`))
	rec := httptest.NewRecorder()
	h.PatchConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var resp tripReachedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.RadiusMeters != 200 {
		t.Fatalf("expected radius_meters=200, got %v", resp.RadiusMeters)
	}
	if !resp.RequireReachedBeforeComplete {
		t.Fatal("expected require_reached_before_complete=true")
	}
	if stub.putTo == nil {
		t.Fatal("expected Put to be called")
	}
	if stub.putTo.RadiusMeters != 200 || !stub.putTo.RequireReachedBeforeComplete {
		t.Fatalf("Put got %+v", stub.putTo)
	}
}

func TestPatchTripReached_MissingField(t *testing.T) {
	h := newTestTripReachedHandlers(&stubTripReachedConfigStore{})

	cases := []string{
		`{}`,
		`{"radius_meters":200}`,
		`{"require_reached_before_complete":true}`,
	}
	for _, body := range cases {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/config/drop-reached", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.PatchConfig(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: expected 400, got %d (%s)", body, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "INVALID_REQUEST") {
			t.Fatalf("body %s: response = %s, want INVALID_REQUEST", body, rec.Body.String())
		}
	}
}

func TestPatchTripReached_RadiusNonPositive(t *testing.T) {
	h := newTestTripReachedHandlers(&stubTripReachedConfigStore{})

	cases := []string{
		`{"radius_meters":0,"require_reached_before_complete":false}`,
		`{"radius_meters":-1,"require_reached_before_complete":true}`,
	}
	for _, body := range cases {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/config/drop-reached", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.PatchConfig(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: expected 400, got %d (%s)", body, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "INVALID_REQUEST") {
			t.Fatalf("body %s: response = %s, want INVALID_REQUEST", body, rec.Body.String())
		}
	}
}

func TestPatchTripReached_InvalidJSON(t *testing.T) {
	h := newTestTripReachedHandlers(&stubTripReachedConfigStore{})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/config/drop-reached", strings.NewReader(`not-json`))
	rec := httptest.NewRecorder()
	h.PatchConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("body = %s, want INVALID_REQUEST", rec.Body.String())
	}
}

func TestGetTripReached_RepoError(t *testing.T) {
	h := newTestTripReachedHandlers(&stubTripReachedConfigStore{getErr: errors.New("ddb down")})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/config/drop-reached", nil)
	rec := httptest.NewRecorder()
	h.GetConfig(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "CONFIG_FETCH_FAILED") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestPatchTripReached_RepoError(t *testing.T) {
	h := newTestTripReachedHandlers(&stubTripReachedConfigStore{putErr: errors.New("ddb down")})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/config/drop-reached", strings.NewReader(`{"radius_meters":200,"require_reached_before_complete":true}`))
	rec := httptest.NewRecorder()
	h.PatchConfig(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "CONFIG_UPDATE_FAILED") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
