package models

import "testing"

func TestCallRecordKeys(t *testing.T) {
	r := &CallRecord{TripID: "T1", CallID: "C2"}
	if r.GetPK() != "TRIP!T1" {
		t.Errorf("PK = %q", r.GetPK())
	}
	if r.GetSK() != "CALL!C2" {
		t.Errorf("SK = %q", r.GetSK())
	}
}
