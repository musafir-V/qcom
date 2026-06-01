package models

import "testing"

func sampleTrip() *Trip {
	return &Trip{
		TripID: "trip-123",
		Tasks: []Task{
			{TaskID: "task-pickup", Type: TaskTypePickup, Status: TaskStatusCreated},
			{TaskID: "task-drop", Type: TaskTypeDrop, Status: TaskStatusCreated, OTP: "4321"},
		},
	}
}

func TestTrip_GetPK(t *testing.T) {
	trip := &Trip{TripID: "trip-123"}
	if got := trip.GetPK(); got != "TRIP!trip-123" {
		t.Errorf("GetPK() = %q, want %q", got, "TRIP!trip-123")
	}
}

func TestTrip_GetSK(t *testing.T) {
	trip := &Trip{TripID: "trip-123"}
	if got := trip.GetSK(); got != "METADATA" {
		t.Errorf("GetSK() = %q, want %q", got, "METADATA")
	}
}

func TestTrip_PickupTask(t *testing.T) {
	trip := sampleTrip()
	pickup := trip.PickupTask()
	if pickup == nil {
		t.Fatal("PickupTask() returned nil")
	}
	if pickup.TaskID != "task-pickup" || pickup.Type != TaskTypePickup {
		t.Errorf("PickupTask() = %+v, want the pickup task", pickup)
	}
}

func TestTrip_PickupTask_None(t *testing.T) {
	trip := &Trip{TripID: "t", Tasks: []Task{{TaskID: "d", Type: TaskTypeDrop}}}
	if got := trip.PickupTask(); got != nil {
		t.Errorf("PickupTask() = %+v, want nil when no pickup task exists", got)
	}
}

func TestTrip_DropTask(t *testing.T) {
	trip := sampleTrip()
	drop := trip.DropTask()
	if drop == nil {
		t.Fatal("DropTask() returned nil")
	}
	if drop.TaskID != "task-drop" || drop.Type != TaskTypeDrop {
		t.Errorf("DropTask() = %+v, want the drop task", drop)
	}
	if drop.OTP != "4321" {
		t.Errorf("DropTask().OTP = %q, want %q", drop.OTP, "4321")
	}
}

func TestTrip_DropTask_None(t *testing.T) {
	trip := &Trip{TripID: "t", Tasks: []Task{{TaskID: "p", Type: TaskTypePickup}}}
	if got := trip.DropTask(); got != nil {
		t.Errorf("DropTask() = %+v, want nil when no drop task exists", got)
	}
}

func TestTrip_TaskByID(t *testing.T) {
	trip := sampleTrip()
	task := trip.TaskByID("task-drop")
	if task == nil {
		t.Fatal("TaskByID(task-drop) returned nil")
	}
	if task.TaskID != "task-drop" {
		t.Errorf("TaskByID() = %+v, want task-drop", task)
	}
}

func TestTrip_TaskByID_NotFound(t *testing.T) {
	trip := sampleTrip()
	if got := trip.TaskByID("missing"); got != nil {
		t.Errorf("TaskByID(missing) = %+v, want nil", got)
	}
}

// Mutating the returned task pointer must mutate the trip's embedded task.
func TestTrip_TaskByID_ReturnsPointerIntoSlice(t *testing.T) {
	trip := sampleTrip()
	trip.TaskByID("task-drop").Status = TaskStatusCompleted
	if trip.Tasks[1].Status != TaskStatusCompleted {
		t.Errorf("expected embedded drop task status to be updated, got %q", trip.Tasks[1].Status)
	}
}
