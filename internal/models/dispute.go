package models

// DisputeStatus is the lifecycle state of a customer dispute.
type DisputeStatus string

const (
	DisputeStatusOpen        DisputeStatus = "OPEN"
	DisputeStatusUnderReview DisputeStatus = "UNDER_REVIEW"
	DisputeStatusResolved    DisputeStatus = "RESOLVED"
	DisputeStatusRejected    DisputeStatus = "REJECTED"
)

const (
	// MaxDisputePhotos is the hard cap on photos attached to a dispute.
	MaxDisputePhotos = 3
	// DisputeDescriptionMinLen applies only when the disposition requires a description.
	DisputeDescriptionMinLen = 10
	DisputeDescriptionMaxLen = 500
	// DisputePhotoKeyPrefix is the object-key namespace dispute photos must live under.
	DisputePhotoKeyPrefix = "disputes"
	// DisputeConfigPK is the shared partition key for dispute config items.
	DisputeConfigPK = "CONFIG"
)

// Dispute is a customer-raised, order-bound complaint.
type Dispute struct {
	DisputeID       string        `json:"dispute_id" dynamodbav:"dispute_id"`
	OrderID         string        `json:"order_id" dynamodbav:"order_id"`
	// DisputeOrderID is the sparse-GSI (DisputeOrderIndex) hash key; equals OrderID.
	// It is a dispute-only attribute so trip items never appear in the index.
	DisputeOrderID  string        `json:"-" dynamodbav:"dispute_order_id"`
	CustomerID      string        `json:"customer_id" dynamodbav:"customer_id"`
	DispositionCode string        `json:"disposition_code" dynamodbav:"disposition_code"`
	Description     string        `json:"description,omitempty" dynamodbav:"description,omitempty"`
	PhotoKeys       []string      `json:"photo_keys,omitempty" dynamodbav:"photo_keys,omitempty"`
	Status          DisputeStatus `json:"status" dynamodbav:"status"`
	ResolutionNote  string        `json:"resolution_note,omitempty" dynamodbav:"resolution_note,omitempty"`
	CreatedAt       string        `json:"created_at" dynamodbav:"created_at"`
	UpdatedAt       string        `json:"updated_at" dynamodbav:"updated_at"`
}

func (d *Dispute) GetPK() string { return "DISPUTE!" + d.DisputeID }
func (d *Dispute) GetSK() string { return "METADATA" }

// DisputeOpenGuardPK is the PK of the uniqueness-guard item that enforces
// one open dispute per order.
func DisputeOpenGuardPK(orderID string) string { return "DISPUTEOPEN!" + orderID }

// DisputeDisposition is a predefined, backend-controlled dispute reason.
type DisputeDisposition struct {
	Code                string `json:"code" dynamodbav:"code"`
	Title               string `json:"title" dynamodbav:"title"`
	Subtitle            string `json:"subtitle,omitempty" dynamodbav:"subtitle,omitempty"`
	PhotosRequired      bool   `json:"photos_required" dynamodbav:"photos_required"`
	PhotoMin            int    `json:"photo_min" dynamodbav:"photo_min"`
	DescriptionRequired bool   `json:"description_required" dynamodbav:"description_required"`
	DisplayOrder        int    `json:"display_order" dynamodbav:"display_order"`
	Active              bool   `json:"active" dynamodbav:"active"`
}

// DisputeDispositionSK is the sort key for a disposition config item under DisputeConfigPK.
func DisputeDispositionSK(code string) string { return "DISPUTE_DISPOSITION!" + code }
