package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
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

type stubDisbursementRepo struct {
	created *models.Disbursement
}

func (s *stubDisbursementRepo) Create(_ context.Context, d *models.Disbursement) error {
	if d.DisbursementID == "" {
		d.DisbursementID = "DB0000000001"
	}
	s.created = d
	return nil
}

type stubDisbursementDERepo struct {
	de            *models.DeliveryExecutive
	updatedPhone  string
	updatedLastAt string
}

func (s *stubDisbursementDERepo) GetByPhone(_ context.Context, _ string) (*models.DeliveryExecutive, error) {
	return s.de, nil
}

func (s *stubDisbursementDERepo) UpdateLastDisbursedAt(_ context.Context, phone, disbursedAt string) error {
	s.updatedPhone = phone
	s.updatedLastAt = disbursedAt
	return nil
}

type stubDisbursementLedgerRepo struct {
	appended []*models.EarningsLedger
}

func (s *stubDisbursementLedgerRepo) Append(_ context.Context, entry *models.EarningsLedger) error {
	s.appended = append(s.appended, entry)
	return nil
}

func TestRecordDisbursement_CreatesEarningsMirror(t *testing.T) {
	disbursementRepo := &stubDisbursementRepo{}
	deRepo := &stubDisbursementDERepo{
		de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971234567"},
	}
	ledgerRepo := &stubDisbursementLedgerRepo{}
	h := &DisbursementHandlers{
		disbursementRepo:   disbursementRepo,
		deRepo:             deRepo,
		earningsLedgerRepo: ledgerRepo,
		logger:             logrus.New(),
	}

	body := map[string]interface{}{
		"amount_zmw":  500.0,
		"period_from": "2026-06-01",
		"period_to":   "2026-06-07",
		"de_phone":    "260971234567",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/de/de-1/disbursement", bytes.NewReader(raw))
	req = mux.SetURLVars(req, map[string]string{"deId": "de-1"})
	rec := httptest.NewRecorder()

	h.RecordDisbursement(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	var got map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	disbursedAt, _ := got["disbursed_at"].(string)
	disbursementID, _ := got["disbursement_id"].(string)
	if disbursedAt == "" || disbursementID == "" {
		t.Fatalf("response missing disbursed_at/disbursement_id: %#v", got)
	}

	if len(ledgerRepo.appended) != 1 {
		t.Fatalf("appended mirror entries = %d, want 1", len(ledgerRepo.appended))
	}
	mirror := ledgerRepo.appended[0]
	if mirror.DEID != "de-1" {
		t.Fatalf("mirror de_id = %q, want de-1", mirror.DEID)
	}
	if mirror.Type != models.EarningTypeDisbursement {
		t.Fatalf("mirror type = %q, want %q", mirror.Type, models.EarningTypeDisbursement)
	}
	if mirror.AmountZMW != -500 {
		t.Fatalf("mirror amount_zmw = %v, want -500", mirror.AmountZMW)
	}
	if mirror.ReferenceID != disbursementID {
		t.Fatalf("mirror reference_id = %q, want %q", mirror.ReferenceID, disbursementID)
	}
	if mirror.Label != "Weekly Payout" {
		t.Fatalf("mirror label = %q, want Weekly Payout", mirror.Label)
	}
	if mirror.CreatedAt != disbursedAt {
		t.Fatalf("mirror created_at = %q, want %q", mirror.CreatedAt, disbursedAt)
	}

	if deRepo.updatedPhone != "+260971234567" {
		t.Fatalf("watermark updated phone = %q, want +260971234567", deRepo.updatedPhone)
	}
	if deRepo.updatedLastAt != disbursedAt {
		t.Fatalf("watermark last_disbursed_at = %q, want %q", deRepo.updatedLastAt, disbursedAt)
	}
}
