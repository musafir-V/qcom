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

func TestRound2ZMW(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{10.0, 10.0}, {10.005, 10.01}, {10.004, 10.00},
		{12.345, 12.35}, {0, 0}, {2.0 * 5.2 * 1.2, 12.48},
		{508.9400000000000182, 508.94},
	}
	for _, c := range cases {
		if got := Round2ZMW(c.in); got != c.want {
			t.Fatalf("Round2ZMW(%v)=%v want %v", c.in, got, c.want)
		}
	}
}

func TestFormatZMW(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{508.9400000000000182, "508.94"},
		{500, "500.00"},
		{0, "0.00"},
		{19.1, "19.10"},
		{226.96, "226.96"},
	}
	for _, c := range cases {
		if got := FormatZMW(c.in); got != c.want {
			t.Fatalf("FormatZMW(%v)=%q want %q", c.in, got, c.want)
		}
	}
}
