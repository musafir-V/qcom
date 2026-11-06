package service

import (
	"testing"

	"github.com/qcom/qcom/internal/models"
)

func TestSynthesizeAdminDropReached_FlagsSynthesizedArrival(t *testing.T) {
	task := &models.Task{Type: models.TaskTypeDrop, Status: models.TaskStatusCreated}
	synthesizeAdminDropReached(task)
	if !task.ReachedByAdmin {
		t.Fatal("ReachedByAdmin = false, want true when admin synthesizes the arrival")
	}
	if task.ReachedAt == "" {
		t.Fatal("ReachedAt not set")
	}
}

func TestSynthesizeAdminDropReached_RiderReachedIsNotFlagged(t *testing.T) {
	task := &models.Task{Type: models.TaskTypeDrop, Status: models.TaskStatusReached, ReachedAt: "2026-11-06T10:12:00+02:00"}
	synthesizeAdminDropReached(task)
	if task.ReachedByAdmin {
		t.Fatal("ReachedByAdmin = true for a rider-marked arrival")
	}
}
