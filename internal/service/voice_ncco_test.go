package service

import (
	"testing"
)

func TestConnectAppNCCO(t *testing.T) {
	ncco := ConnectAppNCCO("cust_U9")
	if len(ncco) != 1 || ncco[0]["action"] != "connect" {
		t.Fatalf("ncco = %+v", ncco)
	}
	eps := ncco[0]["endpoint"].([]map[string]any)
	if eps[0]["type"] != "app" || eps[0]["user"] != "cust_U9" {
		t.Fatalf("endpoint = %+v", eps[0])
	}
}

func TestRejectNCCO(t *testing.T) {
	cases := []struct {
		reason  string
		wantMsg string
	}{
		{"cap_exceeded", "You have reached the maximum number of calls allowed for this delivery."},
		{"trip_not_callable", "Calls are not available for this order at this time."},
		{"trip_not_found", "This order could not be found."},
		{"unknown_caller", "You are not authorised to make this call."},
		{"bad_request", "The person you are calling is unavailable."},
		{"", "The person you are calling is unavailable."},
	}
	for _, tc := range cases {
		ncco := RejectNCCO(tc.reason)
		if ncco[0]["action"] != "talk" {
			t.Fatalf("reason=%q: expected talk action, got %+v", tc.reason, ncco[0])
		}
		if ncco[0]["text"] != tc.wantMsg {
			t.Fatalf("reason=%q: got text %q, want %q", tc.reason, ncco[0]["text"], tc.wantMsg)
		}
	}
}
