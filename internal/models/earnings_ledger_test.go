package models

import "testing"

func TestEarningsLedgerKeys(t *testing.T) {
	e := &EarningsLedger{
		DEID:      "+260971234567",
		EarningID: "earn-123",
		CreatedAt: "2026-06-02T04:00:00Z",
	}
	if got, want := e.GetPK(), "EARN!+260971234567"; got != want {
		t.Errorf("GetPK() = %q, want %q", got, want)
	}
	if got, want := e.GetSK(), "2026-06-02T04:00:00Z#earn-123"; got != want {
		t.Errorf("GetSK() = %q, want %q", got, want)
	}
}
