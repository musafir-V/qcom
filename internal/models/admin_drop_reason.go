package models

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Admin drop-complete reasons. Must stay in sync with ADMIN_DROP_REASONS in
// the admin-dashboard. The server rejects anything outside this set.
const (
	AdminDropReasonOTPNotProvided   = "otp_not_provided"
	AdminDropReasonInternalPurchase = "internal_purchase"
	AdminDropReasonBatchedOrder     = "batched_order"
	AdminDropReasonNotListed        = "not_listed"
	AdminDropReasonNoteMaxRunes     = 500
)

var adminDropReasonLabels = map[string]string{
	AdminDropReasonOTPNotProvided:   "OTP Not Provided",
	AdminDropReasonInternalPurchase: "Internal Purchase",
	AdminDropReasonBatchedOrder:     "Batched Order",
	AdminDropReasonNotListed:        "Not Listed",
}

var (
	ErrInvalidAdminDropReason      = errors.New("invalid admin drop reason")
	ErrAdminDropReasonNoteRequired = errors.New("admin drop reason note required")
	ErrAdminDropReasonNoteTooLong  = errors.New("admin drop reason note too long")
)

// AdminDropReason is the DynamoDB record for an admin "Mark Delivered" reason.
// PK = ADMINDROP!{orderID}, SK = REASON. Overwritten if ops retry.
type AdminDropReason struct {
	OrderID       string `json:"order_id" dynamodbav:"order_id"`
	ReasonCode    string `json:"reason_code" dynamodbav:"reason_code"`
	Note          string `json:"note,omitempty" dynamodbav:"note,omitempty"`
	Reason        string `json:"reason" dynamodbav:"reason"`
	AdminUsername string `json:"admin_username" dynamodbav:"admin_username"`
	At            string `json:"at" dynamodbav:"at"`
}

func (r *AdminDropReason) GetPK() string { return "ADMINDROP!" + r.OrderID }
func (r *AdminDropReason) GetSK() string { return "REASON" }

// FormatAdminDropReason maps a closed reason code (plus optional note) to the
// string stored on the Java order-event trail and in DynamoDB.
func FormatAdminDropReason(code, note string) (string, error) {
	label, ok := adminDropReasonLabels[code]
	if !ok {
		return "", ErrInvalidAdminDropReason
	}
	if code != AdminDropReasonNotListed {
		return label, nil
	}
	note = strings.TrimSpace(note)
	if note == "" {
		return "", ErrAdminDropReasonNoteRequired
	}
	if utf8.RuneCountInString(note) > AdminDropReasonNoteMaxRunes {
		return "", ErrAdminDropReasonNoteTooLong
	}
	return label + ": " + note, nil
}
