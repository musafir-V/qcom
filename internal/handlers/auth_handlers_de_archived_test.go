package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type fakeDEAuthLookup struct {
	de  *models.DeliveryExecutive
	err error
}

func (f *fakeDEAuthLookup) GetByPhone(context.Context, string) (*models.DeliveryExecutive, error) {
	return f.de, f.err
}

func testAuthLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return l
}

func decodeAuthError(t *testing.T, rec *httptest.ResponseRecorder) ErrorResponse {
	t.Helper()
	var body ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error: %v body=%s", err, rec.Body.String())
	}
	return body
}

func TestInitiateOTP_ArchivedDEForbidden(t *testing.T) {
	h := &AuthHandlers{
		deRepo: &fakeDEAuthLookup{de: &models.DeliveryExecutive{
			PhoneNumber: "+260971000001",
			Archived:    true,
		}},
		logger: testAuthLogger(),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/otp/initiate", strings.NewReader(`{"phone_number":"+260971000001"}`))
	req.Header.Set("X-App-Type", "de")
	rec := httptest.NewRecorder()
	h.InitiateOTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	got := decodeAuthError(t, rec)
	if got.Error.Code != "DE_ARCHIVED" {
		t.Fatalf("code = %q, want DE_ARCHIVED", got.Error.Code)
	}
}

func TestInitiateOTP_DENotFound(t *testing.T) {
	h := &AuthHandlers{deRepo: &fakeDEAuthLookup{}, logger: testAuthLogger()}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/otp/initiate", strings.NewReader(`{"phone_number":"+260971000001"}`))
	req.Header.Set("X-App-Type", "de")
	rec := httptest.NewRecorder()
	h.InitiateOTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeAuthError(t, rec); got.Error.Code != "DE_NOT_FOUND" {
		t.Fatalf("code = %q", got.Error.Code)
	}
}

func TestInitiateOTP_DELookupError(t *testing.T) {
	h := &AuthHandlers{
		deRepo: &fakeDEAuthLookup{err: errors.New("boom")},
		logger: testAuthLogger(),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/otp/initiate", strings.NewReader(`{"phone_number":"+260971000001"}`))
	req.Header.Set("X-App-Type", "de")
	rec := httptest.NewRecorder()
	h.InitiateOTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestVerifyOTPForDE_ArchivedForbidden(t *testing.T) {
	h := &AuthHandlers{
		deRepo: &fakeDEAuthLookup{de: &models.DeliveryExecutive{
			DEID:        "DE1",
			PhoneNumber: "+260971000001",
			Archived:    true,
		}},
		logger: testAuthLogger(),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/otp/verify", nil)
	rec := httptest.NewRecorder()
	h.verifyOTPForDE(rec, req, "+260971000001")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	got := decodeAuthError(t, rec)
	if got.Error.Code != "DE_ARCHIVED" {
		t.Fatalf("code = %q, want DE_ARCHIVED", got.Error.Code)
	}
}
