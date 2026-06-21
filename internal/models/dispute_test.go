// internal/models/dispute_test.go
package models

import "testing"

func TestDisputeKeys(t *testing.T) {
	d := &Dispute{DisputeID: "abc"}
	if got := d.GetPK(); got != "DISPUTE!abc" {
		t.Errorf("GetPK() = %q, want DISPUTE!abc", got)
	}
	if got := d.GetSK(); got != "METADATA" {
		t.Errorf("GetSK() = %q, want METADATA", got)
	}
}

func TestDisputeOpenGuardPK(t *testing.T) {
	if got := DisputeOpenGuardPK("order-7"); got != "DISPUTEOPEN!order-7" {
		t.Errorf("DisputeOpenGuardPK() = %q, want DISPUTEOPEN!order-7", got)
	}
}

func TestDisputeDispositionSK(t *testing.T) {
	if got := DisputeDispositionSK("ITEM_MISSING"); got != "DISPUTE_DISPOSITION!ITEM_MISSING" {
		t.Errorf("DisputeDispositionSK() = %q, want DISPUTE_DISPOSITION!ITEM_MISSING", got)
	}
}
