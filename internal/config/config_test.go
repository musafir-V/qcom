package config

import (
	"os"
	"testing"
)

// setRequiredEnv sets the minimum env vars Load() needs to succeed
// and returns a cleanup function.
func setRequiredEnv(t *testing.T) func() {
	t.Helper()
	os.Setenv("JWT_SECRET_KEY", "test-secret-key-at-least-32-bytes!!")
	os.Setenv("TWILIO_ACCOUNT_SID", "ACtest")
	os.Setenv("TWILIO_AUTH_TOKEN", "test-token")
	os.Setenv("TWILIO_VERIFY_SERVICE_SID", "VAtest")
	return func() {
		os.Unsetenv("JWT_SECRET_KEY")
		os.Unsetenv("TWILIO_ACCOUNT_SID")
		os.Unsetenv("TWILIO_AUTH_TOKEN")
		os.Unsetenv("TWILIO_VERIFY_SERVICE_SID")
	}
}

func TestDisputeEligibleStatuses_Default(t *testing.T) {
	cleanup := setRequiredEnv(t)
	defer cleanup()
	os.Unsetenv("DISPUTE_ELIGIBLE_ORDER_STATUSES")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Dispute.EligibleOrderStatuses) != 1 || cfg.Dispute.EligibleOrderStatuses[0] != "DELIVERED" {
		t.Fatalf("default = %v, want [DELIVERED]", cfg.Dispute.EligibleOrderStatuses)
	}
}

func TestDisputeEligibleStatuses_FromEnv(t *testing.T) {
	cleanup := setRequiredEnv(t)
	defer cleanup()
	os.Setenv("DISPUTE_ELIGIBLE_ORDER_STATUSES", "DELIVERED, COMPLETED ")
	defer os.Unsetenv("DISPUTE_ELIGIBLE_ORDER_STATUSES")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	got := cfg.Dispute.EligibleOrderStatuses
	if len(got) != 2 || got[0] != "DELIVERED" || got[1] != "COMPLETED" {
		t.Fatalf("got %v, want [DELIVERED COMPLETED]", got)
	}
}

func TestVoiceConfigLoadsFromEnv(t *testing.T) {
	t.Setenv("VONAGE_VOICE_APP_ID", "app-123")
	t.Setenv("VONAGE_VOICE_PRIVATE_KEY", "cGVt") // base64("pem")
	t.Setenv("VONAGE_VOICE_SIGNATURE_SECRET", "sek")
	got := loadVoiceConfig()
	if got.AppID != "app-123" || got.PrivateKeyB64 != "cGVt" || got.SignatureSecret != "sek" {
		t.Fatalf("voice config = %+v", got)
	}
}
