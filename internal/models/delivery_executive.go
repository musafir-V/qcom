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
	NRCURL           string `json:"nrc_url" dynamodbav:"nrc_url"`
	DriverLicenseURL string `json:"driver_license_url" dynamodbav:"driver_license_url"`
	Status      DEStatus `json:"status" dynamodbav:"status"`
	// Set to "DE_ELIGIBLE#{storeId}" when eligible, cleared otherwise.
	// Used by DEDutyIndex GSI to let the assignment cron query eligible DEs by store.
	DutyIndexKey        string `json:"duty_index_key,omitempty" dynamodbav:"duty_index_key,omitempty"`
	CurrentStoreID      string `json:"current_store_id,omitempty" dynamodbav:"current_store_id,omitempty"`
	CurrentOrderID      string `json:"current_order_id,omitempty" dynamodbav:"current_order_id,omitempty"`
	CurrentTripID       string `json:"current_trip_id,omitempty" dynamodbav:"current_trip_id,omitempty"`
	TotalTripsCompleted int    `json:"total_trips_completed" dynamodbav:"total_trips_completed"`
	DailyTripCount      int    `json:"daily_trip_count" dynamodbav:"daily_trip_count"`
	DailyCountDate      string `json:"daily_count_date,omitempty" dynamodbav:"daily_count_date,omitempty"`
	InHandCashZMW       float64 `json:"in_hand_cash_zmw" dynamodbav:"in_hand_cash_zmw"`
	LastDisbursedAt     string `json:"last_disbursed_at,omitempty" dynamodbav:"last_disbursed_at,omitempty"`
	ReferralCode        string `json:"referral_code,omitempty" dynamodbav:"referral_code,omitempty"`
	CreatedAt           string `json:"created_at" dynamodbav:"created_at"`
	UpdatedAt           string `json:"updated_at" dynamodbav:"updated_at"`
}

func (de *DeliveryExecutive) GetPK() string {
	return "DE!" + de.PhoneNumber
}

func (de *DeliveryExecutive) GetSK() string {
	return "METADATA"
}

// TripsToday returns the DE's completed trip count for `today` (Zambia date,
// "2006-01-02"). DailyTripCount only resets when a trip completes on a new day,
// so a stale DailyCountDate means zero trips have happened today.
func (de *DeliveryExecutive) TripsToday(today string) int {
	if de.DailyCountDate != today {
		return 0
	}
	return de.DailyTripCount
}

// CashExceeds reports whether the DE's in-hand COD cash is strictly above the
// given limit. At exactly the limit the DE is still eligible.
func (de *DeliveryExecutive) CashExceeds(limit float64) bool {
	return de.InHandCashZMW > limit
}
