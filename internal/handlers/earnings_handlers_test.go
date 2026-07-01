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
			{EarningID: "e-ref", DEID: "de-1", Type: models.EarningTypeReferralBonus, AmountZMW: 25, CreatedAt: "2026-06-22T09:30:00+02:00", ReferenceID: "de-ref"},
			{EarningID: "e-kind", DEID: "de-1", Type: models.EarningTypeMealieBag, AmountZMW: 0, Label: "Mealie Bag", CreatedAt: "2026-06-22T10:00:00+02:00", ReferenceID: "2026-W25"},
			{EarningID: "e-disb", DEID: "de-1", Type: models.EarningTypeDisbursement, AmountZMW: -50, Label: "Weekly Payout", CreatedAt: "2026-06-22T11:00:00+02:00", ReferenceID: "disb-1"},
		},
	}
	h := &EarningsHandlers{
		earningsLedgerRepo: ledgerRepo,
		disbursementRepo:   &stubEarningsDisbursementRepo{},
		inKindDisbRepo:     &stubInKindDisbursementRepo{},
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

	if body.OutstandingBalanceZMW != 155 {
		t.Fatalf("outstanding_balance_zmw = %v, want 155", body.OutstandingBalanceZMW)
	}
	if body.LiveOrderTotalZMW != 100 {
		t.Fatalf("live_order_total_zmw = %v, want 100", body.LiveOrderTotalZMW)
	}
	if body.BonusTotalZMW != 55 {
		t.Fatalf("bonus_total_zmw = %v, want 55", body.BonusTotalZMW)
	}

	if len(body.LineItems) != 5 {
		t.Fatalf("line_items length = %d, want 5", len(body.LineItems))
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

// --- In-kind disbursement stub and test ---

type stubInKindDisbursementRepo struct {
	items []*models.InKindDisbursement
}

func (s *stubInKindDisbursementRepo) ListByDE(_ context.Context, _ string) ([]*models.InKindDisbursement, error) {
	return s.items, nil
}

func TestGetEarningsSummary_InKindSummary(t *testing.T) {
	ledgerRepo := &stubEarningsLedgerRepo{
		entries: []*models.EarningsLedger{
			{EarningID: "e1", DEID: "de-1", Type: models.EarningTypeMealieBag, AmountZMW: 0, Label: "Mealie Bag", CreatedAt: "2026-06-01T10:00:00Z", ReferenceID: "2026-W22"},
			{EarningID: "e2", DEID: "de-1", Type: models.EarningTypeMealieBag, AmountZMW: 0, Label: "Mealie Bag", CreatedAt: "2026-06-08T10:00:00Z", ReferenceID: "2026-W23"},
			{EarningID: "e3", DEID: "de-1", Type: models.EarningTypeHouseholdItem, AmountZMW: 0, Label: "Household Item", CreatedAt: "2026-06-08T10:00:00Z", ReferenceID: "2026-W23"},
		},
	}
	inKindDisbRepo := &stubInKindDisbursementRepo{
		items: []*models.InKindDisbursement{
			{DEID: "de-1", SKU: models.InKindSKUMealieBag, Quantity: 1, DisbursedAt: "2026-06-10T09:00:00Z"},
		},
	}
	h := &EarningsHandlers{
		earningsLedgerRepo: ledgerRepo,
		disbursementRepo:   &stubEarningsDisbursementRepo{},
		inKindDisbRepo:     inKindDisbRepo,
		deRepo:             &stubEarningsDERepo{de: &models.DeliveryExecutive{DEID: "de-1", PhoneNumber: "+260971234567"}},
		logger:             logrus.New(),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/de/earnings/summary", nil)
	req = req.WithContext(context.WithValue(req.Context(), "phone", "+260971234567"))
	rec := httptest.NewRecorder()
	h.GetEarningsSummary(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		InKindSummary []struct {
			SKU         string `json:"sku"`
			Earned      int    `json:"earned"`
			Disbursed   int    `json:"disbursed"`
			Outstanding int    `json:"outstanding"`
		} `json:"in_kind_summary"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(body.InKindSummary) != 2 {
		t.Fatalf("want 2 in_kind_summary entries, got %d: %+v", len(body.InKindSummary), body.InKindSummary)
	}

	var bagItem, houseItem *struct {
		SKU         string `json:"sku"`
		Earned      int    `json:"earned"`
		Disbursed   int    `json:"disbursed"`
		Outstanding int    `json:"outstanding"`
	}
	for i := range body.InKindSummary {
		item := &body.InKindSummary[i]
		switch item.SKU {
		case "mealie_bag":
			bagItem = item
		case "household_item":
			houseItem = item
		}
	}

	if bagItem == nil {
		t.Fatal("missing mealie_bag in in_kind_summary")
	}
	if bagItem.Earned != 2 || bagItem.Disbursed != 1 || bagItem.Outstanding != 1 {
		t.Errorf("mealie_bag: earned=%d disbursed=%d outstanding=%d", bagItem.Earned, bagItem.Disbursed, bagItem.Outstanding)
	}
	if houseItem == nil {
		t.Fatal("missing household_item in in_kind_summary")
	}
	if houseItem.Earned != 1 || houseItem.Disbursed != 0 || houseItem.Outstanding != 1 {
		t.Errorf("household_item: earned=%d disbursed=%d outstanding=%d", houseItem.Earned, houseItem.Disbursed, houseItem.Outstanding)
	}
}
