package service

import (
	"testing"

	"github.com/qcom/qcom/internal/models"
)

func TestValidateTaskTransition_PickupArrivedToCompleted(t *testing.T) {
	task := models.Task{Type: models.TaskTypePickup, Status: models.TaskStatusArrived}
	if err := validateTaskTransition(task, models.TaskStatusCompleted, ""); err != nil {
		t.Fatalf("expected valid transition, got: %v", err)
	}
}

func TestValidateTaskTransition_PickupCreatedToArrived_Rejected(t *testing.T) {
	task := models.Task{Type: models.TaskTypePickup, Status: models.TaskStatusCreated}
	if err := validateTaskTransition(task, models.TaskStatusArrived, ""); err == nil {
		t.Fatal("expected error: arrived is cron-only, not via API")
	}
}

func TestValidateTaskTransition_PickupSkipToCompleted_Rejected(t *testing.T) {
	task := models.Task{Type: models.TaskTypePickup, Status: models.TaskStatusCreated}
	if err := validateTaskTransition(task, models.TaskStatusCompleted, ""); err == nil {
		t.Fatal("expected error: cannot skip arrived state")
	}
}

func TestValidateTaskTransition_DropCreatedToReached_ValidOTP(t *testing.T) {
	task := models.Task{Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "4821"}
	if err := validateTaskTransition(task, models.TaskStatusReached, "4821"); err != nil {
		t.Fatalf("expected valid OTP transition, got: %v", err)
	}
}

func TestValidateTaskTransition_DropCreatedToReached_WrongOTP(t *testing.T) {
	task := models.Task{Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "4821"}
	if err := validateTaskTransition(task, models.TaskStatusReached, "9999"); err == nil {
		t.Fatal("expected error: wrong OTP")
	}
}

func TestValidateTaskTransition_DropReachedToCompleted(t *testing.T) {
	task := models.Task{Type: models.TaskTypeDrop, Status: models.TaskStatusReached}
	if err := validateTaskTransition(task, models.TaskStatusCompleted, ""); err != nil {
		t.Fatalf("expected valid transition, got: %v", err)
	}
}

func TestValidateTaskTransition_DropSkip_Rejected(t *testing.T) {
	task := models.Task{Type: models.TaskTypeDrop, Status: models.TaskStatusCreated}
	if err := validateTaskTransition(task, models.TaskStatusCompleted, ""); err == nil {
		t.Fatal("expected error: cannot skip reached state")
	}
}

func TestValidateTaskTransition_ReEnterSameState_Rejected(t *testing.T) {
	task := models.Task{Type: models.TaskTypePickup, Status: models.TaskStatusCompleted}
	if err := validateTaskTransition(task, models.TaskStatusCompleted, ""); err == nil {
		t.Fatal("expected error: re-entering completed state")
	}
}

func TestCrossTaskOrdering_DropBlockedUntilPickupComplete(t *testing.T) {
	trip := &models.Trip{
		Tasks: []models.Task{
			{Type: models.TaskTypePickup, Status: models.TaskStatusArrived},
			{Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
		},
	}
	dropTask := trip.DropTask()
	if err := validateCrossTaskOrdering(trip, dropTask); err == nil {
		t.Fatal("expected error: pickup not completed yet")
	}
}

func TestCrossTaskOrdering_DropAllowedAfterPickupComplete(t *testing.T) {
	trip := &models.Trip{
		Tasks: []models.Task{
			{Type: models.TaskTypePickup, Status: models.TaskStatusCompleted},
			{Type: models.TaskTypeDrop, Status: models.TaskStatusCreated},
		},
	}
	dropTask := trip.DropTask()
	if err := validateCrossTaskOrdering(trip, dropTask); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}
