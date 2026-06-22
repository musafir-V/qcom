package money

import "math"

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
