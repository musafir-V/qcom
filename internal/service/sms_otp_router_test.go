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
	if shouldSendViaAfricaTalking("+254712345678", false, true) {
		t.Fatal("non-Zambia → no AT send")
	}
}

func TestIsZambiaPhone(t *testing.T) {
	tests := []struct {
		phone string
		want  bool
	}{
		{"+260778210256", true},
		{"+260768737229", true},
		{" +260968210256", true},
		{"+918882946897", false},
		{"+254712345678", false},
		{"260778210256", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isZambiaPhone(tt.phone); got != tt.want {
			t.Fatalf("isZambiaPhone(%q) = %v, want %v", tt.phone, got, tt.want)
		}
	}
}
