package models

import "testing"

func TestSMSOTPRoutingConfig_Keys(t *testing.T) {
	cfg := SMSOTPRoutingConfig{ForceTwilio: true}
	if cfg.GetPK() != "CONFIG" {
		t.Fatalf("GetPK() = %q, want CONFIG", cfg.GetPK())
	}
	if cfg.GetSK() != "SMS_OTP_ROUTING_V1" {
		t.Fatalf("GetSK() = %q, want SMS_OTP_ROUTING_V1", cfg.GetSK())
	}
}
