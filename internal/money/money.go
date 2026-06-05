package money

import "math"

// RoundUpZMW rounds an amount up to the nearest whole kwacha.
func RoundUpZMW(amount float64) float64 {
	if amount <= 0 {
		return 0
	}
	return math.Ceil(amount)
}
