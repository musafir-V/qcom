package handlers

import (
	"testing"

	"github.com/qcom/qcom/internal/models"
)

func TestDarkstoreDTO_IncludesActivationFields(t *testing.T) {
	ds := &models.Darkstore{
		DarkstoreID: "001",
		Name:        "Test",
		Latitude:    12.9,
		Longitude:   77.6,
		OpensAt:     "07:00",
		ClosesAt:    "23:00",
	}
	dto := darkstoreDTO(ds)

	ready, ok := dto["activation_ready"].(bool)
	if !ok {
		t.Fatalf("expected activation_ready to be a bool, got %T", dto["activation_ready"])
	}
	if ready {
		t.Fatal("expected activation_ready to be false (no polygon)")
	}

	blockers, ok := dto["activation_blockers"].([]string)
	if !ok {
		t.Fatalf("expected activation_blockers to be []string, got %T", dto["activation_blockers"])
	}
	if len(blockers) == 0 {
		t.Fatal("expected at least one blocker (missing polygon)")
	}

	if dto["darkstore_id"] != "001" {
		t.Fatalf("unexpected darkstore_id: %v", dto["darkstore_id"])
	}
}
