package models

type TripStatus string
type TaskType string
type TaskStatus string

const (
	TripStatusCreated   TripStatus = "created"
	TripStatusAssigned  TripStatus = "assigned"
	TripStatusInTransit TripStatus = "in_transit"
	TripStatusReached   TripStatus = "reached"
	TripStatusCompleted TripStatus = "completed"
	TripStatusCancelled TripStatus = "cancelled"

	TaskTypePickup TaskType = "pickup"
	TaskTypeDrop   TaskType = "drop"

	TaskStatusCreated   TaskStatus = "created"
	TaskStatusArrived   TaskStatus = "arrived"
	TaskStatusReached   TaskStatus = "reached"
	TaskStatusCompleted TaskStatus = "completed"
)

type Task struct {
	TaskID  string     `json:"task_id" dynamodbav:"task_id"`
	Type    TaskType   `json:"type" dynamodbav:"type"`
	Status  TaskStatus `json:"status" dynamodbav:"status"`
	Phone   string     `json:"phone" dynamodbav:"phone"`
	Address string     `json:"address" dynamodbav:"address"`
	Lat     float64    `json:"lat" dynamodbav:"lat"`
	Lng     float64    `json:"lng" dynamodbav:"lng"`
	OTP     string     `json:"otp,omitempty" dynamodbav:"otp,omitempty"` // drop task only
}

type Trip struct {
	TripID  string     `json:"trip_id" dynamodbav:"trip_id"`
	OrderID string     `json:"order_id" dynamodbav:"order_id"`
	StoreID string     `json:"store_id" dynamodbav:"store_id"`
	DEID    string     `json:"de_id,omitempty" dynamodbav:"de_id,omitempty"`
	Status  TripStatus `json:"status" dynamodbav:"status"`
	Tasks   []Task     `json:"tasks" dynamodbav:"tasks"`

	// Payout — set at creation (base) and completion (bonus+total)
	DistanceKM        float64 `json:"distance_km" dynamodbav:"distance_km"`
	BasePayZMW        float64 `json:"base_pay_zmw" dynamodbav:"base_pay_zmw"`
	BonusPayZMW       float64 `json:"bonus_pay_zmw" dynamodbav:"bonus_pay_zmw"`
	TotalPayZMW       float64 `json:"total_pay_zmw" dynamodbav:"total_pay_zmw"`
	DeliveryRankOfDay int     `json:"delivery_rank_of_day" dynamodbav:"delivery_rank_of_day"`

	CreatedAt   string `json:"created_at" dynamodbav:"created_at"`
	UpdatedAt   string `json:"updated_at" dynamodbav:"updated_at"`
	AssignedAt  string `json:"assigned_at,omitempty" dynamodbav:"assigned_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty" dynamodbav:"completed_at,omitempty"`
	CancelledAt string `json:"cancelled_at,omitempty" dynamodbav:"cancelled_at,omitempty"`
}

func (t *Trip) GetPK() string { return "TRIP!" + t.TripID }
func (t *Trip) GetSK() string { return "METADATA" }

// PickupTask returns the pickup task from the embedded list.
func (t *Trip) PickupTask() *Task {
	for i := range t.Tasks {
		if t.Tasks[i].Type == TaskTypePickup {
			return &t.Tasks[i]
		}
	}
	return nil
}

// DropTask returns the drop task from the embedded list.
func (t *Trip) DropTask() *Task {
	for i := range t.Tasks {
		if t.Tasks[i].Type == TaskTypeDrop {
			return &t.Tasks[i]
		}
	}
	return nil
}

// TaskByID returns the task with the given ID.
func (t *Trip) TaskByID(taskID string) *Task {
	for i := range t.Tasks {
		if t.Tasks[i].TaskID == taskID {
			return &t.Tasks[i]
		}
	}
	return nil
}
