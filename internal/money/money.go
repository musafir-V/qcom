package money

import (
	"math"
	"strconv"
)

// RoundUpZMW rounds an amount up to the nearest whole kwacha.
func RoundUpZMW(amount float64) float64 {
	if amount <= 0 {
		return 0
	}
	return math.Ceil(amount)
}

// Round2ZMW rounds to 2 decimal places, half-up.
func Round2ZMW(amount float64) float64 {
	return math.Round(amount*100) / 100
}

// FormatZMW formats a ZMW amount as a DynamoDB Number string with exactly
// two decimal places. Always round first so float64 binary noise (e.g.
// 508.9400000000000182) never gets persisted.
func FormatZMW(amount float64) string {
	return strconv.FormatFloat(Round2ZMW(amount), 'f', 2, 64)
}

// CashMatchEpsilonZMW is the tolerance used when optimistic-locking in-hand
// cash. Half a ngwee absorbs DynamoDB/float64 binary noise without allowing a
// real concurrent COD accrual (≥ 0.01) to slip through.
const CashMatchEpsilonZMW = 0.005
