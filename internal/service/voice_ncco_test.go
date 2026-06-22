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
	ncco := RejectNCCO("trip_not_callable")
	if ncco[0]["action"] != "talk" {
		t.Fatalf("expected talk, got %+v", ncco[0])
	}
}
