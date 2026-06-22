package service

import (
	"testing"
)

func TestVonageUserRoundTrip(t *testing.T) {
	if got := RiderVonageUser("D1"); got != "de_D1" {
		t.Fatalf("rider = %q", got)
	}
	if got := CustomerVonageUser("U9"); got != "cust_U9" {
		t.Fatalf("cust = %q", got)
	}
	kind, id, ok := ParseVonageUser("cust_U9")
	if !ok || kind != "cust" || id != "U9" {
		t.Fatalf("parse = %q %q %v", kind, id, ok)
	}
	if _, _, ok := ParseVonageUser("bogus"); ok {
		t.Fatalf("bogus should not parse")
	}
}
