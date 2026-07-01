package models

type InKindSKU string

const (
	InKindSKUMealieBag     InKindSKU = "mealie_bag"
	InKindSKUHouseholdItem InKindSKU = "household_item"
)

func ValidInKindSKU(sku InKindSKU) bool {
	switch sku {
	case InKindSKUMealieBag, InKindSKUHouseholdItem:
		return true
	}
	return false
}

// InKindDisbursement records a physical handover of in-kind rewards to a DE.
// PK = INKIND_DISB!{deId}, SK = {disbursedAt}#{disbursementId}
type InKindDisbursement struct {
	DEID           string    `json:"de_id" dynamodbav:"de_id"`
	DisbursementID string    `json:"disbursement_id" dynamodbav:"disbursement_id"`
	SKU            InKindSKU `json:"sku" dynamodbav:"sku"`
	Quantity       int       `json:"quantity" dynamodbav:"quantity"`
	Notes          string    `json:"notes,omitempty" dynamodbav:"notes,omitempty"`
	DisbursedBy    string    `json:"disbursed_by" dynamodbav:"disbursed_by"`
	DisbursedAt    string    `json:"disbursed_at" dynamodbav:"disbursed_at"`
}

func (d *InKindDisbursement) GetPK() string { return "INKIND_DISB!" + d.DEID }
func (d *InKindDisbursement) GetSK() string { return d.DisbursedAt + "#" + d.DisbursementID }
