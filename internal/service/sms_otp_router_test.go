package service

import "testing"

func TestShouldSendViaAfricaTalking(t *testing.T) {
	if !shouldSendViaAfricaTalking(false, true) {
		t.Fatal("AT configured + force false → AT")
	}
	if shouldSendViaAfricaTalking(true, true) {
		t.Fatal("force_twilio → Twilio even when AT configured")
	}
	if shouldSendViaAfricaTalking(false, false) {
		t.Fatal("AT not configured → Twilio")
	}
	if shouldSendViaAfricaTalking(true, false) {
		t.Fatal("force_twilio + AT unconfigured → Twilio")
	}
}
