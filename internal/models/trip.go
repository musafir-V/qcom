package models

import "time"

type TripStatus string
type TaskType string
type TaskStatus string

const (
	TripStatusCreated        TripStatus = "created"
	TripStatusAssigned       TripStatus = "assigned"
	TripStatusAccepted       TripStatus = "accepted"
	TripStatusOutForDelivery TripStatus = "out_for_delivery"
	TripStatusCompleted      TripStatus = "completed"
	TripStatusCancelled      TripStatus = "cancelled"

	TaskTypePickup TaskType = "pickup"
	TaskTypeDrop   TaskType = "drop"

	TaskStatusCreated   TaskStatus = "created"
	TaskStatusCompleted TaskStatus = "completed"
)

type TripItem struct {
	Name     string `json:"name" dynamodbav:"name"`
	ImageURL string `json:"image_url" dynamodbav:"image_url"`
	Quantity int    `json:"quantity" dynamodbav:"quantity"`
	Sku      string `json:"sku,omitempty" dynamodbav:"sku,omitempty"`
}

type Payment struct {
	CollectCash bool    `json:"collect_cash" dynamodbav:"collect_cash"`
	AmountZMW   float64 `json:"amount_zmw" dynamodbav:"amount_zmw"`
	Currency    string  `json:"currency,omitempty" dynamodbav:"currency,omitempty"`
}

type Task struct {
	TaskID        string     `json:"task_id" dynamodbav:"task_id"`
	Type          TaskType   `json:"type" dynamodbav:"type"`
	Status        TaskStatus `json:"status" dynamodbav:"status"`
	CreatedAt     string     `json:"created_at,omitempty" dynamodbav:"created_at,omitempty"`
	CompletedAt   string     `json:"completed_at,omitempty" dynamodbav:"completed_at,omitempty"`
	Phone         string     `json:"phone" dynamodbav:"phone"`
	Address       string     `json:"address" dynamodbav:"address"`
	Lat           float64    `json:"lat" dynamodbav:"lat"`
	Lng           float64    `json:"lng" dynamodbav:"lng"`
	OTP           string     `json:"otp,omitempty" dynamodbav:"otp,omitempty"`                       // drop task only
	RecipientName string     `json:"recipient_name,omitempty" dynamodbav:"recipient_name,omitempty"` // drop task only
	PhotoS3Key    string     `json:"photo_s3_key,omitempty" dynamodbav:"photo_s3_key,omitempty"`     // set when driver uploads a verification photo
}

type Trip struct {
	TripID  string     `json:"trip_id" dynamodbav:"trip_id"`
	OrderID string     `json:"order_id" dynamodbav:"order_id"`
	// TripOrderID is the sparse OrderIndex GSI hash key; equals OrderID.
	// Only trip items set this attribute so VOICECTX and other types never pollute the index.
	TripOrderID string     `json:"-" dynamodbav:"trip_order_id,omitempty"`
	StoreID     string     `json:"store_id" dynamodbav:"store_id"`
	DEID    string     `json:"de_id,omitempty" dynamodbav:"de_id,omitempty"`
	DEPhone string     `json:"de_phone,omitempty" dynamodbav:"de_phone,omitempty"`
	Status  TripStatus `json:"status" dynamodbav:"status"`
	Tasks   []Task     `json:"tasks" dynamodbav:"tasks"`
	Items   []TripItem `json:"items,omitempty" dynamodbav:"items,omitempty"`
	Payment *Payment   `json:"payment,omitempty" dynamodbav:"payment,omitempty"`

	// Payout — set at creation (base) and completion (bonus+total)
	DistanceKM        float64 `json:"distance_km" dynamodbav:"distance_km"`
	BasePayZMW        float64 `json:"base_pay_zmw" dynamodbav:"base_pay_zmw"`
	BonusPayZMW       float64 `json:"bonus_pay_zmw" dynamodbav:"bonus_pay_zmw"`
	TotalPayZMW       float64 `json:"total_pay_zmw" dynamodbav:"total_pay_zmw"`
	SLAMinutes        float64 `json:"sla_minutes" dynamodbav:"sla_minutes"`
	OnTime            bool    `json:"on_time" dynamodbav:"on_time"`
	RateRuleID        string  `json:"rate_rule_id,omitempty" dynamodbav:"rate_rule_id,omitempty"`
	RateRuleVersion   int     `json:"rate_rule_version,omitempty" dynamodbav:"rate_rule_version,omitempty"`
	RateMultiplier    float64 `json:"rate_multiplier,omitempty" dynamodbav:"rate_multiplier,omitempty"`
	RateFlatZMW       float64 `json:"rate_flat_zmw,omitempty" dynamodbav:"rate_flat_zmw,omitempty"`
	DeliveryRankOfDay int     `json:"delivery_rank_of_day" dynamodbav:"delivery_rank_of_day"`

	CustomerUserID string `json:"customer_user_id,omitempty" dynamodbav:"customer_user_id,omitempty"`

	CreatedAt   string `json:"created_at" dynamodbav:"created_at"`
	UpdatedAt   string `json:"updated_at" dynamodbav:"updated_at"`
	AssignedAt     string   `json:"assigned_at,omitempty" dynamodbav:"assigned_at,omitempty"`
	AcceptDeadline string   `json:"accept_deadline,omitempty" dynamodbav:"accept_deadline,omitempty"`
	RejectedDEIDs  []string `json:"rejected_de_ids,omitempty" dynamodbav:"rejected_de_ids,omitempty"`
	CompletedAt    string   `json:"completed_at,omitempty" dynamodbav:"completed_at,omitempty"`
	CancelledAt    string   `json:"cancelled_at,omitempty" dynamodbav:"cancelled_at,omitempty"`
}

func (t *Trip) GetPK() string { return "TRIP!" + t.TripID }
func (t *Trip) GetSK() string { return "METADATA" }

// SyncIndexKeys copies OrderID into TripOrderID before persisting a trip.
func (t *Trip) SyncIndexKeys() {
	if id := t.OrderID; id != "" {
		t.TripOrderID = id
	}
}

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

// ActualDeliveryMinutes returns actual delivery minutes and whether both timestamps exist.
func (t *Trip) ActualDeliveryMinutes() (float64, bool) {
	p, d := t.PickupTask(), t.DropTask()
	if p == nil || d == nil || p.CompletedAt == "" || d.CompletedAt == "" {
		return 0, false
	}
	ps, err1 := time.Parse(time.RFC3339, p.CompletedAt)
	ds, err2 := time.Parse(time.RFC3339, d.CompletedAt)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return ds.Sub(ps).Minutes(), true
}

// IsValidTripTransition reports whether a trip may move from `from` to `to`.
// This is the single source of truth for the trip state machine; any
// transition not listed here is illegal.
func IsValidTripTransition(from, to TripStatus) bool {
	switch from {
	case TripStatusCreated:
		return to == TripStatusAssigned || to == TripStatusAccepted || to == TripStatusCancelled
	case TripStatusAssigned:
		return to == TripStatusAccepted || to == TripStatusCreated || to == TripStatusCancelled
	case TripStatusAccepted:
		return to == TripStatusOutForDelivery || to == TripStatusCancelled
	case TripStatusOutForDelivery:
		return to == TripStatusCompleted || to == TripStatusCancelled
	default:
		return false
	}
}

// HasRejected reports whether the given DE has already rejected this trip.
func (t *Trip) HasRejected(deID string) bool {
	for _, id := range t.RejectedDEIDs {
		if id == deID {
			return true
		}
	}
	return false
}
