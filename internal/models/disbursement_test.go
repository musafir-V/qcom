package models

import "testing"

func TestDisbursementKeys(t *testing.T) {
	d := &Disbursement{
		DEID:           "+260971234567",
		DisbursementID: "disb-456",
		DisbursedAt:    "2026-06-01T10:30:00Z",
	}
	if got, want := d.GetPK(), "DISBURSEMENT!+260971234567"; got != want {
		t.Errorf("GetPK() = %q, want %q", got, want)
	}
	if got, want := d.GetSK(), "2026-06-01T10:30:00Z#disb-456"; got != want {
		t.Errorf("GetSK() = %q, want %q", got, want)
	}
}
