package service

import "testing"

func TestShouldSendViaAfricaTalking(t *testing.T) {
	if !shouldSendViaAfricaTalking("+260778210256", false, true) {
		t.Fatal("Airtel + AT configured + force false → AT")
	}
	if !shouldSendViaAfricaTalking("+260768737229", false, true) {
		t.Fatal("MTN 76 → AT")
	}
	if !shouldSendViaAfricaTalking("+260968210256", false, true) {
		t.Fatal("MTN 96 → AT")
	}
	if shouldSendViaAfricaTalking("+260778210256", true, true) {
		t.Fatal("force_twilio → Twilio even for AT-eligible phones")
	}
	if shouldSendViaAfricaTalking("+260778210256", false, false) {
		t.Fatal("AT not configured → Twilio")
	}
	if shouldSendViaAfricaTalking("+918882946897", false, true) {
		t.Fatal("+91 → no AT send (bypass OTP)")
	}
}

func TestIsIndiaPhone(t *testing.T) {
	tests := []struct {
		phone string
		want  bool
	}{
		{"+918882946897", true},
		{"+919515365236", true},
		{" +917766066119", true},
		{"+260778210256", false},
		{"918882946897", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isIndiaPhone(tt.phone); got != tt.want {
			t.Fatalf("isIndiaPhone(%q) = %v, want %v", tt.phone, got, tt.want)
		}
	}
}
