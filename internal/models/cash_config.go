package models

// DefaultCashLimitZMW is the fallback in-hand cash cap (K500) used when no
// CashConfig row exists or the stored value is non-positive.
const DefaultCashLimitZMW = 500.0

// CashConfig holds the in-hand cash limit, stored as a singleton DynamoDB item
// (PK=CONFIG, SK=CASH_V1) so ops can tune it live without a redeploy.
type CashConfig struct {
	LimitZMW float64 `json:"limit_zmw" dynamodbav:"limit_zmw"`
}

func (c *CashConfig) GetPK() string { return "CONFIG" }
func (c *CashConfig) GetSK() string { return "CASH_V1" }

// EffectiveLimitZMW returns the configured limit, or the default when
// unset/non-positive.
func (c *CashConfig) EffectiveLimitZMW() float64 {
	if c.LimitZMW <= 0 {
		return DefaultCashLimitZMW
	}
	return c.LimitZMW
}
