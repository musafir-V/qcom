package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type stubEarningsLedgerRepo struct {
	entries []*models.EarningsLedger
}

func (s *stubEarningsLedgerRepo) QueryByDE(_ context.Context, _ string, _ string, _ int32, _ map[string]types.AttributeValue) ([]*models.EarningsLedger, map[string]types.AttributeValue, error) {
	return s.entries, nil, nil
}

func (s *stubEarningsLedgerRepo) SumByDEAfter(_ context.Context, _ string, _ string) (float64, error) {
	var total float64
	for _, e := range s.entries {
		total += e.AmountZMW
	}
	return total, nil
}

type stubEarningsDisbursementRepo struct{}

func (s *stubEarningsDisbursementRepo) ListByDE(_ context.Context, _ string) ([]*models.Disbursement, error) {
	return nil, nil
}

type stubEarningsDERepo struct {
	de *models.DeliveryExecutive
}

func (s *stubEarningsDERepo) GetByPhone(_ context.Context, _ string) (*models.DeliveryExecutive, error) {
	return s.de, nil
}

func TestGetEarningsSummary_IncludesLabelsAndExcludesNonCashFromOutstanding(t *testing.T) {
	ledgerRepo := &stubEarningsLedgerRepo{
		entries: []*models.EarningsLedger{
			{EarningID: "e-trip", DEID: "de-1", Type: models.EarningTypeTrip, AmountZMW: 100, CreatedAt: "2026-06-22T08:00:00+02:00", ReferenceID: "trip-1"},
			{EarningID: "e-b1", DEID: "de-1", Type: models.EarningTypeB1DailyBonus, AmountZMW: 30, CreatedAt: "2026-06-22T09:00:00+02:00", ReferenceID: "2026-06-22"},
			{EarningID: "e-kind", DEID: "de-1", Type: models.EarningTypeMealieBag, AmountZMW: 0, Label: "Mealie Bag", CreatedAt: "2026-06-22T10:00:00+02:00", ReferenceID: "2026-W25"},
			{EarningID: "e-disb", DEID: "de-1", Type: models.EarningTypeDisbursement, AmountZMW: -50, Label: "Weekly Payout", CreatedAt: "2026-06-22T11:00:00+02:00", ReferenceID: "disb-1"},
		},
	}
	h := &EarningsHandlers{
		earningsLedgerRepo: ledgerRepo,
		disbursementRepo:   &stubEarningsDisbursementRepo{},
		deRepo:             &stubEarningsDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971234567"}},
		logger:             logrus.New(),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/de/earnings/summary", nil)
	req = req.WithContext(context.WithValue(req.Context(), "phone", "+260971234567"))
	rec := httptest.NewRecorder()

	h.GetEarningsSummary(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		OutstandingBalanceZMW float64 `json:"outstanding_balance_zmw"`
		LiveOrderTotalZMW     float64 `json:"live_order_total_zmw"`
		BonusTotalZMW         float64 `json:"bonus_total_zmw"`
		LineItems             []struct {
			Type      string  `json:"type"`
			AmountZMW float64 `json:"amount_zmw"`
			Label     string  `json:"label"`
		} `json:"line_items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body.OutstandingBalanceZMW != 130 {
		t.Fatalf("outstanding_balance_zmw = %v, want 130", body.OutstandingBalanceZMW)
	}
	if body.LiveOrderTotalZMW != 100 {
		t.Fatalf("live_order_total_zmw = %v, want 100", body.LiveOrderTotalZMW)
	}
	if body.BonusTotalZMW != 30 {
		t.Fatalf("bonus_total_zmw = %v, want 30", body.BonusTotalZMW)
	}

	if len(body.LineItems) != 4 {
		t.Fatalf("line_items length = %d, want 4", len(body.LineItems))
	}

	labelsByType := map[string]string{}
	for _, item := range body.LineItems {
		labelsByType[item.Type] = item.Label
	}
	if labelsByType[string(models.EarningTypeMealieBag)] != "Mealie Bag" {
		t.Fatalf("mealie_bag label = %q, want Mealie Bag", labelsByType[string(models.EarningTypeMealieBag)])
	}
	if labelsByType[string(models.EarningTypeDisbursement)] != "Weekly Payout" {
		t.Fatalf("disbursement label = %q, want Weekly Payout", labelsByType[string(models.EarningTypeDisbursement)])
	}
}
