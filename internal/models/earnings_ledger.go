package models

type EarningType string

const (
	EarningTypeTrip          EarningType = "trip"
	EarningTypeWeeklyBonus   EarningType = "weekly_bonus"
	EarningTypeReferralBonus EarningType = "referral_bonus"
	EarningTypeB1DailyBonus  EarningType = "b1_daily_bonus"
	EarningTypeMealieBag     EarningType = "mealie_bag"
	EarningTypeHouseholdItem EarningType = "household_item"
	EarningTypeWeeklyGift    EarningType = "weekly_gift"
	EarningTypeDisbursement  EarningType = "disbursement"
)

// EarningsLedger is one earning event for a DE.
// PK = EARN!{deId}, SK = {created_at}#{earning_id}
type EarningsLedger struct {
	DEID        string      `json:"de_id" dynamodbav:"de_id"`
	EarningID   string      `json:"earning_id" dynamodbav:"earning_id"`
	Type        EarningType `json:"type" dynamodbav:"type"`
	AmountZMW   float64     `json:"amount_zmw" dynamodbav:"amount_zmw"`
	Label       string      `json:"label,omitempty" dynamodbav:"label,omitempty"`
	CreatedAt   string      `json:"created_at" dynamodbav:"created_at"`
	ReferenceID string      `json:"reference_id" dynamodbav:"reference_id"`
	DistanceKM  float64     `json:"distance_km,omitempty" dynamodbav:"distance_km,omitempty"`
}

func (e *EarningsLedger) GetPK() string { return "EARN!" + e.DEID }
func (e *EarningsLedger) GetSK() string { return e.CreatedAt + "#" + e.EarningID }
