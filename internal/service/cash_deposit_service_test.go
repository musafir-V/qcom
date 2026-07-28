package service

import (
	"errors"
	"testing"
)

func TestValidateDepositAmount(t *testing.T) {
	if err := ValidateDepositAmount(100); err != nil {
		t.Fatalf("positive amount should be valid, got %v", err)
	}
	if err := ValidateDepositAmount(0); !errors.Is(err, ErrInvalidDepositAmount) {
		t.Fatalf("zero should be invalid, got %v", err)
	}
	if err := ValidateDepositAmount(-5); !errors.Is(err, ErrInvalidDepositAmount) {
		t.Fatalf("negative should be invalid, got %v", err)
	}
}

func TestClampDeposit(t *testing.T) {
	cases := []struct {
		name        string
		requested   float64
		inHand      float64
		wantApplied float64
		wantNew     float64
	}{
		{"partial deposit", 200, 500, 200, 300},
		{"full deposit to zero", 500, 500, 500, 0},
		{"overpayment clamps to zero", 800, 500, 500, 0},
		{"deposit with zero in-hand", 100, 0, 0, 0},
		{"polluted float in-hand rounds cleanly", 508.94, 508.9400000000000182, 508.94, 0},
		{"partial against polluted float", 500, 508.9400000000000182, 500, 8.94},
		{"fractional request rounds", 100.005, 200, 100.01, 99.99},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			applied, newBal := ClampDeposit(c.requested, c.inHand)
			if applied != c.wantApplied || newBal != c.wantNew {
				t.Fatalf("ClampDeposit(%v,%v) = (%v,%v), want (%v,%v)",
					c.requested, c.inHand, applied, newBal, c.wantApplied, c.wantNew)
			}
		})
	}
}
