package service

import "testing"

func TestIsMTNZambiaTwilioPrefix(t *testing.T) {
	tests := []struct {
		phone string
		want  bool
	}{
		{"+260768737229", true},
		{"+260968210256", true},
		{"+260761234567", true},
		{"+260961234567", true},
		{"+260778210256", false}, // Airtel 77
		{"+260978210256", false}, // Airtel 97
		{"+260758210256", false}, // Zamtel 75
		{"+260958210256", false}, // Zamtel 95
		{"+918882946897", false},
		{"260768737229", false}, // missing +
		{"", false},
	}
	for _, tt := range tests {
		if got := isMTNZambiaTwilioPrefix(tt.phone); got != tt.want {
			t.Fatalf("isMTNZambiaTwilioPrefix(%q) = %v, want %v", tt.phone, got, tt.want)
		}
	}
}

func TestShouldSendViaAfricaTalking(t *testing.T) {
	if !shouldSendViaAfricaTalking("+260778210256", false, true) {
		t.Fatal("Airtel + AT configured + force false → AT")
	}
	if shouldSendViaAfricaTalking("+260768737229", false, true) {
		t.Fatal("MTN 76 → Twilio")
	}
	if shouldSendViaAfricaTalking("+260968210256", false, true) {
		t.Fatal("MTN 96 → Twilio")
	}
	if shouldSendViaAfricaTalking("+260778210256", true, true) {
		t.Fatal("force_twilio → Twilio even for non-MTN")
	}
	if shouldSendViaAfricaTalking("+260778210256", false, false) {
		t.Fatal("AT not configured → Twilio")
	}
}
