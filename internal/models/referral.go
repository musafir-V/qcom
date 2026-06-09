package models

type ReferralStatus string

const (
	ReferralStatusActive    ReferralStatus = "active"
	ReferralStatusCompleted ReferralStatus = "completed"
	ReferralStatusExpired   ReferralStatus = "expired"
)

type Referral struct {
	ReferrerDEID      string         `json:"referrer_de_id" dynamodbav:"referrer_de_id"`
	ReferredDEID      string         `json:"referred_de_id" dynamodbav:"referred_de_id"`
	ReferredName      string         `json:"referred_name,omitempty" dynamodbav:"referred_name,omitempty"`
	Status            ReferralStatus `json:"status" dynamodbav:"status"`
	CreatedAt         string         `json:"created_at" dynamodbav:"created_at"`
	WindowExpiresAt   string         `json:"window_expires_at" dynamodbav:"window_expires_at"`
	PayoutTriggeredAt string         `json:"payout_triggered_at,omitempty" dynamodbav:"payout_triggered_at,omitempty"`
}

func (r *Referral) GetPK() string { return "REFERRAL!" + r.ReferredDEID }
func (r *Referral) GetSK() string { return "METADATA" }
