package models

import "testing"

func TestCashDepositLedgerKeys(t *testing.T) {
	e := &CashDepositLedger{
		DEID:      "+260971234567",
		DepositID: "dep-789",
	}
	if got, want := e.GetPK(), "CASHDEP!+260971234567"; got != want {
		t.Errorf("GetPK() = %q, want %q", got, want)
	}
	if got, want := e.GetSK(), "dep-789"; got != want {
		t.Errorf("GetSK() = %q, want %q", got, want)
	}
}
