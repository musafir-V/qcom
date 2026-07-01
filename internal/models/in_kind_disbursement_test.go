package models

import "testing"

func TestInKindDisbursement_GetPK(t *testing.T) {
	d := &InKindDisbursement{DEID: "DE0001234567"}
	if got := d.GetPK(); got != "INKIND_DISB!DE0001234567" {
		t.Errorf("GetPK = %q, want %q", got, "INKIND_DISB!DE0001234567")
	}
}

func TestInKindDisbursement_GetSK(t *testing.T) {
	d := &InKindDisbursement{DisbursedAt: "2026-07-02T10:00:00Z", DisbursementID: "IK0001234567"}
	want := "2026-07-02T10:00:00Z#IK0001234567"
	if got := d.GetSK(); got != want {
		t.Errorf("GetSK = %q, want %q", got, want)
	}
}

func TestValidInKindSKU(t *testing.T) {
	cases := []struct {
		sku   InKindSKU
		valid bool
	}{
		{InKindSKUMealieBag, true},
		{InKindSKUHouseholdItem, true},
		{"weekly_gift", false},
		{"cash", false},
		{"", false},
	}
	for _, c := range cases {
		if got := ValidInKindSKU(c.sku); got != c.valid {
			t.Errorf("ValidInKindSKU(%q) = %v, want %v", c.sku, got, c.valid)
		}
	}
}
