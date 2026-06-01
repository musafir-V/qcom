package models

// Disbursement records an offline payout from Bunzo to a DE.
// PK = DISBURSEMENT!{deId}, SK = {disbursedAt}#{disbursementId}
type Disbursement struct {
	DEID           string  `json:"de_id" dynamodbav:"de_id"`
	DisbursementID string  `json:"disbursement_id" dynamodbav:"disbursement_id"`
	AmountZMW      float64 `json:"amount_zmw" dynamodbav:"amount_zmw"`
	PeriodFrom     string  `json:"period_from" dynamodbav:"period_from"`
	PeriodTo       string  `json:"period_to" dynamodbav:"period_to"`
	DisbursedAt    string  `json:"disbursed_at" dynamodbav:"disbursed_at"`
}

func (d *Disbursement) GetPK() string { return "DISBURSEMENT!" + d.DEID }
func (d *Disbursement) GetSK() string { return d.DisbursedAt + "#" + d.DisbursementID }
