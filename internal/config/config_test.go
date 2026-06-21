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
	os.Setenv("VONAGE_APP_ID", "test-app-id")
	os.Setenv("VONAGE_PRIVATE_KEY", "dGVzdA==")
	os.Setenv("VONAGE_WHATSAPP_FROM", "test-from")
	return func() {
		os.Unsetenv("JWT_SECRET_KEY")
		os.Unsetenv("VONAGE_APP_ID")
		os.Unsetenv("VONAGE_PRIVATE_KEY")
		os.Unsetenv("VONAGE_WHATSAPP_FROM")
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
