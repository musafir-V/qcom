package service

import (
	"testing"
	"time"
)

func TestGenerateReferralCode_IsNumeric(t *testing.T) {
	for i := 0; i < 100; i++ {
		code := generateReferralCode()
		if len(code) != 6 {
			t.Fatalf("expected 6-digit code, got %q (len %d)", code, len(code))
		}
		for _, c := range code {
			if c < '0' || c > '9' {
				t.Fatalf("expected numeric code, got %q", code)
			}
		}
	}
}

func TestIsWithinReferralWindow_Inside(t *testing.T) {
	createdAt := time.Now().UTC().Add(-5 * 24 * time.Hour).Format(time.RFC3339)
	if !isWithinReferralWindow(createdAt, 10) {
		t.Fatal("expected to be within 10-day window after 5 days")
	}
}

func TestIsWithinReferralWindow_Outside(t *testing.T) {
	createdAt := time.Now().UTC().Add(-11 * 24 * time.Hour).Format(time.RFC3339)
	if isWithinReferralWindow(createdAt, 10) {
		t.Fatal("expected to be outside 10-day window after 11 days")
	}
}

// An unparseable timestamp must never count as "within window" — it would
// otherwise let a malformed referral trigger a bonus indefinitely.
func TestIsWithinReferralWindow_InvalidTimestamp(t *testing.T) {
	if isWithinReferralWindow("not-a-timestamp", 10) {
		t.Fatal("expected invalid timestamp to be treated as outside the window")
	}
}
