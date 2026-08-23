package models

// CashDepositLedger is one cash-deposit event for a DE, recorded when a driver
// deposits collected COD cash back at a hub. Stored in its own partition
// (PK=CASHDEP!{phone}) so it never appears in earnings queries (which key on
// EARN!{deId}). DEID is the rider phone, not the prefixed DE203… id. SK is the
// deposit_id for idempotent retries.
type CashDepositLedger struct {
	DEID               string  `json:"de_id" dynamodbav:"de_id"`
	DepositID          string  `json:"deposit_id" dynamodbav:"deposit_id"`
	RequestedAmountZMW float64 `json:"requested_amount_zmw" dynamodbav:"requested_amount_zmw"`
	AppliedAmountZMW   float64 `json:"applied_amount_zmw" dynamodbav:"applied_amount_zmw"`
	CreatedAt          string  `json:"created_at" dynamodbav:"created_at"`
}

func (e *CashDepositLedger) GetPK() string { return "CASHDEP!" + e.DEID }
func (e *CashDepositLedger) GetSK() string { return e.DepositID }
