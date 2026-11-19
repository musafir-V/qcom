package punctuality

import (
	"testing"
	"time"
)

func TestOpenTotal_SumsDaysAfterLastClear(t *testing.T) {
	loc := time.FixedZone("CAT", 2*60*60)
	days := []ScoredDay{
		{ScoredAt: time.Date(2026, 6, 20, 8, 0, 0, 0, loc), SignedMinutes: -30},
		{ScoredAt: time.Date(2026, 6, 21, 8, 0, 0, 0, loc), SignedMinutes: 10},
		{ScoredAt: time.Date(2026, 6, 22, 8, 0, 0, 0, loc), SignedMinutes: -15},
	}
	clearedAt := time.Date(2026, 6, 21, 18, 0, 0, 0, loc)

	if got := OpenTotal(days, clearedAt); got != -15 {
		t.Fatalf("OpenTotal() = %d, want -15", got)
	}
}

func TestOpenTotal_SumsEveryDayWhenNeverCleared(t *testing.T) {
	loc := time.FixedZone("CAT", 2*60*60)
	days := []ScoredDay{
		{ScoredAt: time.Date(2026, 6, 20, 8, 0, 0, 0, loc), SignedMinutes: -30},
		{ScoredAt: time.Date(2026, 6, 21, 8, 0, 0, 0, loc), SignedMinutes: 10},
	}
	if got := OpenTotal(days, time.Time{}); got != -20 {
		t.Fatalf("OpenTotal() = %d, want -20", got)
	}
}

func TestValidateClear_RejectsZero(t *testing.T) {
	if err := ValidateClear(0, "cut money"); err == nil {
		t.Fatal("expected clear of a zero total to be rejected")
	}
}

func TestValidateClear_RejectsBlankNote(t *testing.T) {
	if err := ValidateClear(-20, "  "); err == nil {
		t.Fatal("expected a blank note to be rejected")
	}
}

func TestValidateClear_AllowsEarlyAndLateTotals(t *testing.T) {
	if err := ValidateClear(-20, "deducted late time"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateClear(15, "reset early credit"); err != nil {
		t.Fatal(err)
	}
}
