package money

import "testing"

func TestRoundUpZMW(t *testing.T) {
	tests := []struct {
		in, want float64
	}{
		{0, 0},
		{-5, 0},
		{15, 15},
		{12.5, 13},
		{54.57, 55},
		{54.001, 55},
		{0.01, 1},
	}
	for _, tc := range tests {
		if got := RoundUpZMW(tc.in); got != tc.want {
			t.Fatalf("RoundUpZMW(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
