package models

type DEStatus string

const (
	DEStatusOffline  DEStatus = "offline"
	DEStatusEligible DEStatus = "eligible"
	DEStatusBusy     DEStatus = "busy"
	DEStatusFree     DEStatus = "free"
)

type DeliveryExecutive struct {
	DEID        string   `json:"de_id" dynamodbav:"de_id"`
	PhoneNumber string   `json:"phone_number" dynamodbav:"phone_number"`
	Name        string   `json:"name" dynamodbav:"name"`
	ProfileURL  string   `json:"profile_url" dynamodbav:"profile_url"`
	NRCURL      string   `json:"nrc_url" dynamodbav:"nrc_url"`
	Status      DEStatus `json:"status" dynamodbav:"status"`
	// Set to "DE_ELIGIBLE#{storeId}" when eligible, cleared otherwise.
	// Used by DEDutyIndex GSI to let the assignment cron query eligible DEs by store.
	DutyIndexKey   string `json:"duty_index_key,omitempty" dynamodbav:"duty_index_key,omitempty"`
	CurrentStoreID string `json:"current_store_id,omitempty" dynamodbav:"current_store_id,omitempty"`
	CurrentOrderID string `json:"current_order_id,omitempty" dynamodbav:"current_order_id,omitempty"`
	ReferralCode   string `json:"referral_code,omitempty" dynamodbav:"referral_code,omitempty"`
	CreatedAt      string `json:"created_at" dynamodbav:"created_at"`
	UpdatedAt      string `json:"updated_at" dynamodbav:"updated_at"`
}

func (de *DeliveryExecutive) GetPK() string {
	return "DE!" + de.PhoneNumber
}

func (de *DeliveryExecutive) GetSK() string {
	return "METADATA"
}
