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
	// UnknownStoreID is the sentinel store id stamped on a dispute whose store
	// could not be resolved from its trip. Store ids are numeric, so this can
	// never collide with a real one.
	UnknownStoreID = "UNKNOWN"
)

// Dispute is a customer-raised, order-bound complaint.
type Dispute struct {
	DisputeID   string `json:"dispute_id" dynamodbav:"dispute_id"`
	OrderNumber string `json:"order_number" dynamodbav:"order_number"`
	// DisputeOrderNumber is the sparse-GSI (DisputeOrderIndex) hash key; equals OrderNumber.
	// It is a dispute-only attribute so trip items never appear in the index.
	DisputeOrderNumber string `json:"-" dynamodbav:"dispute_order_number"`
	CustomerID         string `json:"customer_id" dynamodbav:"customer_id"`
	// StoreID is the darkstore the disputed order belonged to, resolved from the
	// trip at creation time. "UNKNOWN" when it could not be resolved; empty on
	// disputes created before store attribution existed (never backfilled).
	StoreID string `json:"store_id,omitempty" dynamodbav:"store_id,omitempty"`
	// DisputeStoreStatusKey is the sparse-GSI (DisputeStoreStatusIndex) hash key;
	// equals "<store_id>#<status>". Empty — and therefore absent from the item —
	// when StoreID is empty, which keeps legacy disputes out of the index.
	DisputeStoreStatusKey string        `json:"-" dynamodbav:"dispute_store_status_key,omitempty"`
	DispositionCode       string        `json:"disposition_code" dynamodbav:"disposition_code"`
	Description           string        `json:"description,omitempty" dynamodbav:"description,omitempty"`
	PhotoKeys             []string      `json:"photo_keys,omitempty" dynamodbav:"photo_keys,omitempty"`
	Status                DisputeStatus `json:"status" dynamodbav:"status"`
	ResolutionNote        string        `json:"resolution_note,omitempty" dynamodbav:"resolution_note,omitempty"`
	// ResolvedBy is the admin username that last changed the status (audit).
	ResolvedBy string `json:"resolved_by,omitempty" dynamodbav:"resolved_by,omitempty"`
	// DisputeStatusKey is the sparse-GSI (DisputeStatusIndex) hash key; equals string(Status).
	// Dispute-only attribute so non-dispute items never appear in the index.
	DisputeStatusKey string `json:"-" dynamodbav:"dispute_status_key,omitempty"`
	CreatedAt        string `json:"created_at" dynamodbav:"created_at"`
	UpdatedAt        string `json:"updated_at" dynamodbav:"updated_at"`
}

func (d *Dispute) GetPK() string { return "DISPUTE!" + d.DisputeID }
func (d *Dispute) GetSK() string { return "METADATA" }

// DisputeOpenGuardPK is the PK of the uniqueness-guard item that enforces at most
// one dispute per order in v1; the guard is not cleared until admin tooling lands.
func DisputeOpenGuardPK(orderNumber string) string { return "DISPUTEOPEN!" + orderNumber }

// DisputeStoreStatusKeyFor builds the DisputeStoreStatusIndex hash key.
// Returns "" when storeID is empty so the attribute is omitted entirely and the
// dispute stays out of the sparse index rather than landing under a bogus key.
func DisputeStoreStatusKeyFor(storeID string, status DisputeStatus) string {
	if storeID == "" {
		return ""
	}
	return storeID + "#" + string(status)
}

// CanTransitionDispute reports whether an admin may move a dispute from `from` to `to`.
// OPEN may go to UNDER_REVIEW/RESOLVED/REJECTED; UNDER_REVIEW may go to RESOLVED/REJECTED;
// RESOLVED and REJECTED are terminal. No-op transitions are rejected.
func CanTransitionDispute(from, to DisputeStatus) bool {
	switch from {
	case DisputeStatusOpen:
		return to == DisputeStatusUnderReview || to == DisputeStatusResolved || to == DisputeStatusRejected
	case DisputeStatusUnderReview:
		return to == DisputeStatusResolved || to == DisputeStatusRejected
	default:
		return false
	}
}

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
