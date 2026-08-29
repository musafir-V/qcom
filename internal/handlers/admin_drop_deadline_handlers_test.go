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

type stubDropDeadlineConfigStore struct {
	cfg    *models.DropDeadlineConfig
	getErr error
	putErr error
	putTo  *models.DropDeadlineConfig
}

func (s *stubDropDeadlineConfigStore) Get(_ context.Context) (*models.DropDeadlineConfig, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.cfg == nil {
		return &models.DropDeadlineConfig{}, nil
	}
	return s.cfg, nil
}

func (s *stubDropDeadlineConfigStore) Put(_ context.Context, cfg *models.DropDeadlineConfig) error {
	if s.putErr != nil {
		return s.putErr
	}
	s.putTo = cfg
	s.cfg = cfg
	return nil
}

func newTestDropDeadlineHandlers(repo dropDeadlineConfigStore) *AdminDropDeadlineHandlers {
	logger := logrus.New()
	logger.SetOutput(logrus.New().Out)
	return newAdminDropDeadlineHandlers(repo, logger)
}

func TestGetDropDeadline_Default(t *testing.T) {
	h := newTestDropDeadlineHandlers(&stubDropDeadlineConfigStore{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/config/drop-deadline", nil)
	rec := httptest.NewRecorder()
	h.GetConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var resp dropDeadlineResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.MinutesPerKm != models.DefaultDropDeadlineMinutesPerKm {
		t.Fatalf("expected minutes_per_km=%v, got %v", models.DefaultDropDeadlineMinutesPerKm, resp.MinutesPerKm)
	}
	if resp.ExtraMinutes != models.DefaultDropDeadlineExtraMinutes {
		t.Fatalf("expected extra_minutes=%v, got %v", models.DefaultDropDeadlineExtraMinutes, resp.ExtraMinutes)
	}
}

func TestPatchDropDeadline_Success(t *testing.T) {
	stub := &stubDropDeadlineConfigStore{}
	h := newTestDropDeadlineHandlers(stub)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/config/drop-deadline", strings.NewReader(`{"minutes_per_km":3,"extra_minutes":5}`))
	rec := httptest.NewRecorder()
	h.PatchConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var resp dropDeadlineResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.MinutesPerKm != 3 {
		t.Fatalf("expected minutes_per_km=3, got %v", resp.MinutesPerKm)
	}
	if resp.ExtraMinutes != 5 {
		t.Fatalf("expected extra_minutes=5, got %v", resp.ExtraMinutes)
	}
	if stub.putTo == nil {
		t.Fatal("expected Put to be called")
	}
	if stub.putTo.MinutesPerKm != 3 || stub.putTo.ExtraMinutes != 5 {
		t.Fatalf("Put got %+v", stub.putTo)
	}
}

func TestPatchDropDeadline_AllowsZeroY(t *testing.T) {
	stub := &stubDropDeadlineConfigStore{}
	h := newTestDropDeadlineHandlers(stub)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/config/drop-deadline", strings.NewReader(`{"minutes_per_km":2,"extra_minutes":0}`))
	rec := httptest.NewRecorder()
	h.PatchConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var resp dropDeadlineResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ExtraMinutes != 0 {
		t.Fatalf("expected extra_minutes=0, got %v", resp.ExtraMinutes)
	}
	if stub.putTo == nil || stub.putTo.ExtraMinutes != 0 {
		t.Fatalf("Put extra_minutes = %+v, want 0", stub.putTo)
	}
}

func TestPatchDropDeadline_RejectsMissingFields(t *testing.T) {
	h := newTestDropDeadlineHandlers(&stubDropDeadlineConfigStore{})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/config/drop-deadline", strings.NewReader(`{"minutes_per_km":3}`))
	rec := httptest.NewRecorder()
	h.PatchConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("response = %s, want INVALID_REQUEST", rec.Body.String())
	}
}

func TestPatchDropDeadline_RejectsNonPositiveX(t *testing.T) {
	h := newTestDropDeadlineHandlers(&stubDropDeadlineConfigStore{})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/config/drop-deadline", strings.NewReader(`{"minutes_per_km":0,"extra_minutes":1}`))
	rec := httptest.NewRecorder()
	h.PatchConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("response = %s, want INVALID_REQUEST", rec.Body.String())
	}
}

func TestNewAdminDropDeadlineHandlers_Constructs(t *testing.T) {
	h := NewAdminDropDeadlineHandlers(nil, logrus.New())
	if h == nil {
		t.Fatal("expected handler")
	}
}

func TestGetDropDeadline_RepoError(t *testing.T) {
	h := newTestDropDeadlineHandlers(&stubDropDeadlineConfigStore{getErr: errors.New("ddb down")})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/config/drop-deadline", nil)
	rec := httptest.NewRecorder()
	h.GetConfig(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "CONFIG_FETCH_FAILED") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestPatchDropDeadline_RepoError(t *testing.T) {
	h := newTestDropDeadlineHandlers(&stubDropDeadlineConfigStore{putErr: errors.New("ddb down")})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/config/drop-deadline", strings.NewReader(`{"minutes_per_km":3,"extra_minutes":5}`))
	rec := httptest.NewRecorder()
	h.PatchConfig(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "CONFIG_UPDATE_FAILED") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestPatchDropDeadline_InvalidJSON(t *testing.T) {
	h := newTestDropDeadlineHandlers(&stubDropDeadlineConfigStore{})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/config/drop-deadline", strings.NewReader(`not-json`))
	rec := httptest.NewRecorder()
	h.PatchConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("body = %s, want INVALID_REQUEST", rec.Body.String())
	}
}

func TestPatchDropDeadline_RejectsNegativeY(t *testing.T) {
	h := newTestDropDeadlineHandlers(&stubDropDeadlineConfigStore{})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/config/drop-deadline", strings.NewReader(`{"minutes_per_km":2,"extra_minutes":-1}`))
	rec := httptest.NewRecorder()
	h.PatchConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("response = %s, want INVALID_REQUEST", rec.Body.String())
	}
}
