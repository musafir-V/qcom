package service

import (
	"context"
	"testing"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/timezone"
)

type stubTodayEarningsLedgerRepo struct {
	entries           []*models.EarningsLedger
	gotDEID           string
	gotAfterTimestamp string
}

func (s *stubTodayEarningsLedgerRepo) SumPositiveCashByDEAfter(_ context.Context, deID, afterTimestamp string) (float64, error) {
	s.gotDEID = deID
	s.gotAfterTimestamp = afterTimestamp

	var total float64
	for _, entry := range s.entries {
		if models.IsPositiveCashEarning(entry) {
			total += entry.AmountZMW
		}
	}
	return total, nil
}

func TestGetTodayEarnings_SumsPositiveCashOnly(t *testing.T) {
	ledgerRepo := &stubTodayEarningsLedgerRepo{
		entries: []*models.EarningsLedger{
			{Type: models.EarningTypeTrip, AmountZMW: 100},
			{Type: models.EarningTypeB1DailyBonus, AmountZMW: 30},
			{Type: models.EarningTypeReferralBonus, AmountZMW: 25},
			{Type: models.EarningTypeMealieBag, AmountZMW: 0},
			{Type: models.EarningTypeDisbursement, AmountZMW: -50},
		},
	}
	svc := &DEService{earningsLedgerRepo: ledgerRepo}

	got, err := svc.GetTodayEarnings(context.Background(), "de-1")
	if err != nil {
		t.Fatalf("GetTodayEarnings returned error: %v", err)
	}
	if got != 155 {
		t.Fatalf("today earnings = %v, want 155", got)
	}
	if ledgerRepo.gotDEID != "de-1" {
		t.Fatalf("deID = %q, want de-1", ledgerRepo.gotDEID)
	}
	if ledgerRepo.gotAfterTimestamp != timezone.StartOfDayString() {
		t.Fatalf("after timestamp = %q, want %q", ledgerRepo.gotAfterTimestamp, timezone.StartOfDayString())
	}
}
