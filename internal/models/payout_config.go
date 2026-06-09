package models

type PayoutConfig struct {
	// Referral
	ReferralTripsThreshold int     `json:"referral_trips_threshold" dynamodbav:"referral_trips_threshold"`
	ReferralWindowDays     int     `json:"referral_window_days" dynamodbav:"referral_window_days"`
	ReferralBonusZMW       float64 `json:"referral_bonus_zmw" dynamodbav:"referral_bonus_zmw"`

	// Distance-based pay (populated by Plan B)
	RatePerKmZMW             float64 `json:"rate_per_km_zmw" dynamodbav:"rate_per_km_zmw"`
	Tier1Threshold           int     `json:"tier1_threshold" dynamodbav:"tier1_threshold"`
	Tier1BonusZMW            float64 `json:"tier1_bonus_zmw" dynamodbav:"tier1_bonus_zmw"`
	Tier2Threshold           int     `json:"tier2_threshold" dynamodbav:"tier2_threshold"`
	Tier2BonusZMW            float64 `json:"tier2_bonus_zmw" dynamodbav:"tier2_bonus_zmw"`
	MilestoneMessageTemplate string  `json:"milestone_message_template" dynamodbav:"milestone_message_template"`

	// Weekly bonus (populated by Plan B)
	MinDeliveriesPerDay int     `json:"min_deliveries_per_day" dynamodbav:"min_deliveries_per_day"`
	WeeklyW1Days        int     `json:"weekly_w1_days" dynamodbav:"weekly_w1_days"`
	WeeklyW1BonusZMW    float64 `json:"weekly_w1_bonus_zmw" dynamodbav:"weekly_w1_bonus_zmw"`
	WeeklyW2Days        int     `json:"weekly_w2_days" dynamodbav:"weekly_w2_days"`
	WeeklyW2BonusZMW    float64 `json:"weekly_w2_bonus_zmw" dynamodbav:"weekly_w2_bonus_zmw"`
	WeeklyW3Days        int     `json:"weekly_w3_days" dynamodbav:"weekly_w3_days"`
	WeeklyW3BonusZMW    float64 `json:"weekly_w3_bonus_zmw" dynamodbav:"weekly_w3_bonus_zmw"`
}

func (p *PayoutConfig) GetPK() string { return "CONFIG" }
func (p *PayoutConfig) GetSK() string { return "PAYOUT_V1" }
