package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/timezone"
)

func TestComputeETA_OnTime(t *testing.T) {
	// Trip created 5 minutes ago
	createdAt := timezone.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	eta := computeETA(createdAt)

	if eta == nil {
		t.Fatal("expected ETA payload, got nil")
	}
	if eta.IsDelayed {
		t.Fatal("expected not delayed at 5 minutes")
	}
	if eta.RemainingMinutes < 9 || eta.RemainingMinutes > 11 {
		t.Fatalf("expected ~10 remaining minutes, got %d", eta.RemainingMinutes)
	}
	if eta.Message != nil {
		t.Fatalf("expected no delay message, got: %s", *eta.Message)
	}
}

func TestComputeETA_Delayed(t *testing.T) {
	// Trip created 20 minutes ago
	createdAt := timezone.Now().Add(-20 * time.Minute).UTC().Format(time.RFC3339)
	eta := computeETA(createdAt)

	if eta == nil {
		t.Fatal("expected ETA payload, got nil")
	}
	if !eta.IsDelayed {
		t.Fatal("expected delayed at 20 minutes")
	}
	if eta.RemainingMinutes != 0 {
		t.Fatalf("expected 0 remaining minutes, got %d", eta.RemainingMinutes)
	}
	if eta.Message == nil {
		t.Fatal("expected delay message")
	}
}

func TestComputeETA_ExactBoundary(t *testing.T) {
	// Trip created exactly 15 minutes ago
	createdAt := timezone.Now().Add(-15 * time.Minute).UTC().Format(time.RFC3339)
	eta := computeETA(createdAt)

	if eta == nil {
		t.Fatal("expected ETA payload")
	}
	if !eta.IsDelayed {
		t.Fatal("expected delayed at exactly 15 minutes")
	}
}

func TestComputeETA_InvalidTimestamp(t *testing.T) {
	eta := computeETA("not-a-timestamp")
	if eta != nil {
		t.Fatal("expected nil for invalid timestamp")
	}
}

func TestEnrichOrderWithTracking_AllPresent(t *testing.T) {
	order := map[string]json.RawMessage{"orderNumber": json.RawMessage(`"ORD1"`)}
	otp := "1234"
	name := "John M."
	eta := &ETAPayload{ExpiresAt: "2026-06-25T10:00:00Z", RemainingMinutes: 8, IsDelayed: false}

	if err := enrichOrderWithTracking(order, &otp, &name, eta); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(order["otp"]) != `"1234"` {
		t.Errorf("otp = %s", order["otp"])
	}
	if string(order["de_name"]) != `"John M."` {
		t.Errorf("de_name = %s", order["de_name"])
	}
	if string(order["orderNumber"]) != `"ORD1"` {
		t.Errorf("original field clobbered: %s", order["orderNumber"])
	}
	var gotETA ETAPayload
	if err := json.Unmarshal(order["eta"], &gotETA); err != nil {
		t.Fatalf("eta not valid json: %v", err)
	}
	if gotETA.RemainingMinutes != 8 {
		t.Errorf("eta.remaining_minutes = %d", gotETA.RemainingMinutes)
	}
}

func TestEnrichOrderWithTracking_NilsBecomeJSONNull(t *testing.T) {
	order := map[string]json.RawMessage{}
	if err := enrichOrderWithTracking(order, nil, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, k := range []string{"otp", "de_name", "eta"} {
		if string(order[k]) != "null" {
			t.Errorf("%s = %s, want null", k, order[k])
		}
	}
}
