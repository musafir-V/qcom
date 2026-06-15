package service

import (
	"errors"
	"testing"

	"github.com/qcom/qcom/internal/models"
)

func TestValidateTaskTransition_CreatedToCompleted(t *testing.T) {
	for _, taskType := range []models.TaskType{models.TaskTypePickup, models.TaskTypeDrop} {
		task := models.Task{Type: taskType, Status: models.TaskStatusCreated}
		if err := validateTaskTransition(task, models.TaskStatusCompleted); err != nil {
			t.Fatalf("%s: expected valid transition, got: %v", taskType, err)
		}
	}
}

func TestValidateTaskTransition_LegacyStatusToCompleted(t *testing.T) {
	// Tasks already in legacy intermediate states can still be completed.
	for _, status := range []models.TaskStatus{"arrived", "reached"} {
		task := models.Task{Type: models.TaskTypeDrop, Status: status}
		if err := validateTaskTransition(task, models.TaskStatusCompleted); err != nil {
			t.Fatalf("status %q: expected valid transition, got: %v", status, err)
		}
	}
}

func TestValidateTaskTransition_NonCompletedTarget_Rejected(t *testing.T) {
	task := models.Task{Type: models.TaskTypePickup, Status: models.TaskStatusCreated}
	if err := validateTaskTransition(task, models.TaskStatus("arrived")); err == nil {
		t.Fatal("expected error: only completed is allowed")
	}
}

func TestValidateTaskTransition_AlreadyCompleted_Rejected(t *testing.T) {
	task := models.Task{Type: models.TaskTypePickup, Status: models.TaskStatusCompleted}
	if err := validateTaskTransition(task, models.TaskStatusCompleted); err == nil {
		t.Fatal("expected error: re-entering completed state")
	}
}

func TestValidateDropOTP_Correct(t *testing.T) {
	task := models.Task{Type: models.TaskTypeDrop, OTP: "1234"}
	if err := validateDropOTP(task, "1234"); err != nil {
		t.Fatalf("expected valid OTP, got: %v", err)
	}
}

func TestValidateDropOTP_Wrong(t *testing.T) {
	task := models.Task{Type: models.TaskTypeDrop, OTP: "1234"}
	err := validateDropOTP(task, "0000")
	if err == nil {
		t.Fatal("expected error for wrong OTP")
	}
	if !errors.Is(err, ErrInvalidOTP) {
		t.Fatalf("expected ErrInvalidOTP, got: %v", err)
	}
}

func TestValidateDropOTP_Missing(t *testing.T) {
	task := models.Task{Type: models.TaskTypeDrop, OTP: "1234"}
	err := validateDropOTP(task, "")
	if err == nil {
		t.Fatal("expected error for missing OTP")
	}
	if !errors.Is(err, ErrInvalidOTP) {
		t.Fatalf("expected ErrInvalidOTP, got: %v", err)
	}
}

func TestValidateTaskAgainstTripStatus(t *testing.T) {
	cases := []struct {
		name     string
		taskType models.TaskType
		status   models.TripStatus
		wantErr  bool
	}{
		{"pickup allowed when accepted", models.TaskTypePickup, models.TripStatusAccepted, false},
		{"pickup blocked when assigned", models.TaskTypePickup, models.TripStatusAssigned, true},
		{"pickup blocked when created", models.TaskTypePickup, models.TripStatusCreated, true},
		{"drop allowed when out_for_delivery", models.TaskTypeDrop, models.TripStatusOutForDelivery, false},
		{"drop blocked when accepted (pickup not done)", models.TaskTypeDrop, models.TripStatusAccepted, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateTaskAgainstTripStatus(c.taskType, c.status)
			if c.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("expected nil, got %v", err)
			}
			if c.wantErr && err != nil && !errors.Is(err, ErrPrerequisiteIncomplete) {
				t.Fatalf("expected ErrPrerequisiteIncomplete, got %v", err)
			}
		})
	}
}

func TestValidatePickupScan_Match(t *testing.T) {
	trip := &models.Trip{Status: models.TripStatusAccepted, OrderID: "ORD-1"}
	if err := validatePickupScan(trip, "ORD-1"); err != nil {
		t.Fatalf("expected valid scan, got: %v", err)
	}
}

func TestValidatePickupScan_Mismatch(t *testing.T) {
	trip := &models.Trip{Status: models.TripStatusAccepted, OrderID: "ORD-1"}
	if err := validatePickupScan(trip, "ORD-2"); !errors.Is(err, ErrPickupOrderMismatch) {
		t.Fatalf("expected ErrPickupOrderMismatch, got: %v", err)
	}
}

func TestValidatePickupScan_Empty(t *testing.T) {
	trip := &models.Trip{Status: models.TripStatusAccepted, OrderID: "ORD-1"}
	if err := validatePickupScan(trip, ""); !errors.Is(err, ErrPickupOrderMismatch) {
		t.Fatalf("expected ErrPickupOrderMismatch, got: %v", err)
	}
}

func TestValidatePickupScan_WrongState(t *testing.T) {
	trip := &models.Trip{Status: models.TripStatusOutForDelivery, OrderID: "ORD-1"}
	if err := validatePickupScan(trip, "ORD-1"); !errors.Is(err, ErrInvalidTripTransition) {
		t.Fatalf("expected ErrInvalidTripTransition, got: %v", err)
	}
}

func TestCodAccrualAmount(t *testing.T) {
	cases := []struct {
		name string
		trip *models.Trip
		want float64
	}{
		{"nil payment", &models.Trip{}, 0},
		{"online prepaid (no cash to collect)", &models.Trip{
			Payment: &models.Payment{CollectCash: false, AmountZMW: 120},
		}, 0},
		{"COD accrues amount", &models.Trip{
			Payment: &models.Payment{CollectCash: true, AmountZMW: 120},
		}, 120},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := codAccrualAmount(c.trip); got != c.want {
				t.Fatalf("codAccrualAmount = %v, want %v", got, c.want)
			}
		})
	}
}
