package handlers

import (
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
