package handlers

import (
	"errors"
	"net/http"
	"testing"

	"github.com/qcom/qcom/internal/models"
)

func TestValidateDisbursementRequest(t *testing.T) {
	valid := disbursementRequest{
		AmountZMW:  100,
		PeriodFrom: "2026-06-01",
		PeriodTo:   "2026-06-14",
		DEPhone:    "+260971234567",
	}

	cases := []struct {
		name    string
		req     disbursementRequest
		wantErr error
	}{
		{"valid", valid, nil},
		{"zero amount", func() disbursementRequest { r := valid; r.AmountZMW = 0; return r }(), errDisbursementInvalidAmount},
		{"negative amount", func() disbursementRequest { r := valid; r.AmountZMW = -1; return r }(), errDisbursementInvalidAmount},
		{"missing period_from", func() disbursementRequest { r := valid; r.PeriodFrom = ""; return r }(), errDisbursementMissingPeriod},
		{"missing period_to", func() disbursementRequest { r := valid; r.PeriodTo = ""; return r }(), errDisbursementMissingPeriod},
		{"period_from after period_to", func() disbursementRequest {
			r := valid
			r.PeriodFrom = "2026-06-15"
			r.PeriodTo = "2026-06-01"
			return r
		}(), errDisbursementInvalidPeriod},
		{"same period_from and period_to", func() disbursementRequest {
			r := valid
			r.PeriodFrom = "2026-06-07"
			r.PeriodTo = "2026-06-07"
			return r
		}(), nil},
		{"missing de_phone", func() disbursementRequest { r := valid; r.DEPhone = ""; return r }(), errDisbursementMissingDEPhone},
		{"whitespace de_phone", func() disbursementRequest { r := valid; r.DEPhone = "   "; return r }(), errDisbursementMissingDEPhone},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDisbursementRequest(tc.req)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error: got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateDEIdentity(t *testing.T) {
	de := &models.DeliveryExecutive{DEID: "abc-123", PhoneNumber: "+260971234567"}

	cases := []struct {
		name     string
		pathDEID string
		de       *models.DeliveryExecutive
		wantErr  error
	}{
		{"match", "abc-123", de, nil},
		{"mismatch", "other-id", de, errDisbursementDEIDMismatch},
		{"nil de", "abc-123", nil, errDisbursementDENotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDEIdentity(tc.pathDEID, tc.de)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error: got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestClassifyDisbursementError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"invalid amount", errDisbursementInvalidAmount, http.StatusBadRequest, "INVALID_AMOUNT"},
		{"missing period", errDisbursementMissingPeriod, http.StatusBadRequest, "MISSING_FIELD"},
		{"invalid period", errDisbursementInvalidPeriod, http.StatusBadRequest, "INVALID_PERIOD"},
		{"missing phone", errDisbursementMissingDEPhone, http.StatusBadRequest, "MISSING_FIELD"},
		{"de id mismatch", errDisbursementDEIDMismatch, http.StatusBadRequest, "DE_ID_MISMATCH"},
		{"de not found", errDisbursementDENotFound, http.StatusNotFound, "DE_NOT_FOUND"},
		{"unknown", errors.New("boom"), http.StatusInternalServerError, "DISBURSEMENT_FAILED"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code := classifyDisbursementError(tc.err)
			if status != tc.wantStatus {
				t.Errorf("status: got %d, want %d", status, tc.wantStatus)
			}
			if code != tc.wantCode {
				t.Errorf("code: got %q, want %q", code, tc.wantCode)
			}
		})
	}
}
