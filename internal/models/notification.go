package models

// RecipientType identifies who receives a push notification.
type RecipientType string

const (
	RecipientTypeDriver   RecipientType = "driver"
	RecipientTypeCustomer RecipientType = "customer"
	RecipientTypePicker   RecipientType = "picker"
)

// NotificationPriority controls FCM transport urgency.
type NotificationPriority string

const (
	PriorityCritical NotificationPriority = "critical"
	PriorityHigh     NotificationPriority = "high"
	PriorityNormal   NotificationPriority = "normal"
)

// NotificationSendRequest is the canonical send contract for all callers.
type NotificationSendRequest struct {
	RecipientType RecipientType            `json:"recipient_type"`
	RecipientID   string                   `json:"recipient_id"`
	EventType     string                   `json:"event_type"`
	Priority      NotificationPriority     `json:"priority"`
	Title         string                   `json:"title"`
	Body          string                   `json:"body"`
	Data          map[string]string        `json:"data"`
}

// NotificationSendStatus describes the outcome of a send attempt.
type NotificationSendStatus string

const (
	SendStatusSent    NotificationSendStatus = "sent"
	SendStatusSkipped NotificationSendStatus = "skipped"
)

// NotificationSendResult is returned from Send and the internal HTTP API.
type NotificationSendResult struct {
	Status    NotificationSendStatus `json:"status"`
	MessageID string                 `json:"message_id,omitempty"`
	Reason    string                 `json:"reason,omitempty"`
}

// DeviceTokenRecord is persisted in DynamoDB by the central token store.
type DeviceTokenRecord struct {
	RecipientType RecipientType `dynamodbav:"recipient_type"`
	RecipientID   string        `dynamodbav:"recipient_id"`
	FCMToken      string        `dynamodbav:"fcm_token"`
	Platform      string        `dynamodbav:"platform,omitempty"`
	UpdatedAt     string        `dynamodbav:"updated_at"`
}

func (r *DeviceTokenRecord) GetPK() string {
	return "NOTIF!" + string(r.RecipientType) + "!" + r.RecipientID
}

func (r *DeviceTokenRecord) GetSK() string {
	return "TOKEN"
}
