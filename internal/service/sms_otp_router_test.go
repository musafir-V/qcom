package service

import "testing"

func TestLastNumericDigit(t *testing.T) {
	tests := []struct {
		phone   string
		want    byte
		wantOK  bool
	}{
		{"+260770990570", '0', true},
		{"+260770990571", '1', true},
		{"+260770990572", '2', true},
		{"+26077099057X", '7', true},
		{"no-digits", 0, false},
		{"", 0, false},
	}
	for _, tt := range tests {
		got, ok := lastNumericDigit(tt.phone)
		if ok != tt.wantOK || got != tt.want {
			t.Fatalf("lastNumericDigit(%q) = (%q, %v), want (%q, %v)", tt.phone, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestShouldSendViaAfricaTalking(t *testing.T) {
	if !shouldSendViaAfricaTalking("+260770990570", false, true) {
		t.Fatal("digit 0 + AT configured + force false → AT")
	}
	if !shouldSendViaAfricaTalking("+260770990571", false, true) {
		t.Fatal("digit 1 + AT configured + force false → AT")
	}
	if shouldSendViaAfricaTalking("+260770990572", false, true) {
		t.Fatal("digit 2 → Twilio")
	}
	if shouldSendViaAfricaTalking("+260770990570", true, true) {
		t.Fatal("force_twilio → Twilio even for digit 0")
	}
	if shouldSendViaAfricaTalking("+260770990570", false, false) {
		t.Fatal("AT not configured → Twilio")
	}
}

func TestPhoneEligibleForLocalOTP(t *testing.T) {
	if !phoneEligibleForLocalOTP("+260770990570") {
		t.Fatal("digit 0 should be eligible")
	}
	if !phoneEligibleForLocalOTP("+260770990571") {
		t.Fatal("digit 1 should be eligible")
	}
	if phoneEligibleForLocalOTP("+260770990579") {
		t.Fatal("digit 9 should not be eligible")
	}
	if phoneEligibleForLocalOTP("no-digits") {
		t.Fatal("no digits should not be eligible")
	}
}

func TestShouldSendViaAfricaTalking_TrailingNonDigit(t *testing.T) {
	// Last numeric digit is 0 even with trailing junk.
	if !shouldSendViaAfricaTalking("+260770990570x", false, true) {
		t.Fatal("trailing non-digit should still route on last numeric digit 0")
	}
}
