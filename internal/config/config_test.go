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

func TestAfricaTalkingConfig_OptionalDefaults(t *testing.T) {
	cleanup := setRequiredEnv(t)
	defer cleanup()
	os.Unsetenv("AFRICASTALKING_USERNAME")
	os.Unsetenv("AFRICASTALKING_API_KEY")
	os.Unsetenv("AFRICASTALKING_BASE_URL")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.AfricaTalking.Username != "" || cfg.AfricaTalking.APIKey != "" {
		t.Fatalf("expected empty AT creds by default, got %+v", cfg.AfricaTalking)
	}
	if cfg.AfricaTalking.BaseURL != "https://api.africastalking.com" {
		t.Fatalf("BaseURL = %q, want default", cfg.AfricaTalking.BaseURL)
	}
}

func TestAfricaTalkingConfig_FromEnv(t *testing.T) {
	cleanup := setRequiredEnv(t)
	defer cleanup()
	t.Setenv("AFRICASTALKING_USERNAME", "Bunzo")
	t.Setenv("AFRICASTALKING_API_KEY", "atsk_test")
	t.Setenv("AFRICASTALKING_BASE_URL", "https://api.sandbox.africastalking.com")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.AfricaTalking.Username != "Bunzo" {
		t.Fatalf("Username = %q", cfg.AfricaTalking.Username)
	}
	if cfg.AfricaTalking.APIKey != "atsk_test" {
		t.Fatalf("APIKey = %q", cfg.AfricaTalking.APIKey)
	}
	if cfg.AfricaTalking.BaseURL != "https://api.sandbox.africastalking.com" {
		t.Fatalf("BaseURL = %q", cfg.AfricaTalking.BaseURL)
	}
}
