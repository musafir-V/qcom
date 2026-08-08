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

type stubSMSOTPRoutingConfigStore struct {
	cfg    *models.SMSOTPRoutingConfig
	getErr error
	setErr error
	setTo  *bool
}

func (s *stubSMSOTPRoutingConfigStore) Get(_ context.Context) (*models.SMSOTPRoutingConfig, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.cfg == nil {
		return &models.SMSOTPRoutingConfig{ForceTwilio: false}, nil
	}
	return s.cfg, nil
}

func (s *stubSMSOTPRoutingConfigStore) SetForceTwilio(_ context.Context, force bool) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.setTo = &force
	s.cfg = &models.SMSOTPRoutingConfig{ForceTwilio: force}
	return nil
}

func newTestSMSOTPRoutingHandlers(repo smsOTPRoutingConfigStore) *AdminSMSOTPRoutingHandlers {
	logger := logrus.New()
	logger.SetOutput(logrus.New().Out)
	return newAdminSMSOTPRoutingHandlers(repo, logger)
}

func TestGetSMSOTPRouting_Default(t *testing.T) {
	h := newTestSMSOTPRoutingHandlers(&stubSMSOTPRoutingConfigStore{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sms-otp-routing", nil)
	rec := httptest.NewRecorder()
	h.GetConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var resp smsOTPRoutingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ForceTwilio {
		t.Fatalf("expected force_twilio=false by default, got true")
	}
}

func TestPutSMSOTPRouting_ForceTwilio(t *testing.T) {
	stub := &stubSMSOTPRoutingConfigStore{}
	h := newTestSMSOTPRoutingHandlers(stub)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/sms-otp-routing", strings.NewReader(`{"force_twilio":true}`))
	rec := httptest.NewRecorder()
	h.PutConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var resp smsOTPRoutingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.ForceTwilio {
		t.Fatalf("expected force_twilio=true, got false")
	}
	if stub.setTo == nil || !*stub.setTo {
		t.Fatalf("expected SetForceTwilio(true) to be called")
	}
}

func TestPutSMSOTPRouting_ClearForceTwilio(t *testing.T) {
	stub := &stubSMSOTPRoutingConfigStore{cfg: &models.SMSOTPRoutingConfig{ForceTwilio: true}}
	h := newTestSMSOTPRoutingHandlers(stub)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/sms-otp-routing", strings.NewReader(`{"force_twilio":false}`))
	rec := httptest.NewRecorder()
	h.PutConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var resp smsOTPRoutingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ForceTwilio {
		t.Fatal("expected force_twilio=false")
	}
	if stub.setTo == nil || *stub.setTo {
		t.Fatal("expected SetForceTwilio(false)")
	}
}

func TestPutSMSOTPRouting_MissingField(t *testing.T) {
	h := newTestSMSOTPRoutingHandlers(&stubSMSOTPRoutingConfigStore{})

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/sms-otp-routing", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.PutConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("body = %s, want INVALID_REQUEST", rec.Body.String())
	}
}

func TestPutSMSOTPRouting_InvalidJSON(t *testing.T) {
	h := newTestSMSOTPRoutingHandlers(&stubSMSOTPRoutingConfigStore{})

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/sms-otp-routing", strings.NewReader(`not-json`))
	rec := httptest.NewRecorder()
	h.PutConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestGetSMSOTPRouting_RepoError(t *testing.T) {
	h := newTestSMSOTPRoutingHandlers(&stubSMSOTPRoutingConfigStore{getErr: errors.New("ddb down")})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sms-otp-routing", nil)
	rec := httptest.NewRecorder()
	h.GetConfig(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "CONFIG_FETCH_FAILED") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestPutSMSOTPRouting_RepoError(t *testing.T) {
	h := newTestSMSOTPRoutingHandlers(&stubSMSOTPRoutingConfigStore{setErr: errors.New("ddb down")})

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/sms-otp-routing", strings.NewReader(`{"force_twilio":true}`))
	rec := httptest.NewRecorder()
	h.PutConfig(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "CONFIG_UPDATE_FAILED") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestGetSMSOTPRouting_ForceTwilioTrue(t *testing.T) {
	h := newTestSMSOTPRoutingHandlers(&stubSMSOTPRoutingConfigStore{
		cfg: &models.SMSOTPRoutingConfig{ForceTwilio: true},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/sms-otp-routing", nil)
	rec := httptest.NewRecorder()
	h.GetConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp smsOTPRoutingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.ForceTwilio {
		t.Fatal("expected force_twilio=true")
	}
}
