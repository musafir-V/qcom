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

func TestOpenBalance_SubtractsAPartialClear(t *testing.T) {
	loc := time.FixedZone("CAT", 2*60*60)
	days := []ScoredDay{
		{ScoredAt: time.Date(2026, 6, 20, 8, 0, 0, 0, loc), SignedMinutes: -150},
		{ScoredAt: time.Date(2026, 6, 22, 8, 0, 0, 0, loc), SignedMinutes: -20},
	}
	settlements := []Settlement{
		{At: time.Date(2026, 6, 20, 18, 0, 0, 0, loc), Total: -90, Partial: true},
	}
	if got := OpenBalance(days, settlements); got != -80 {
		t.Fatalf("OpenBalance() = %d, want -80", got)
	}
}

func TestOpenBalance_FullClearDropsEarlierDaysAndPartials(t *testing.T) {
	loc := time.FixedZone("CAT", 2*60*60)
	days := []ScoredDay{
		{ScoredAt: time.Date(2026, 6, 20, 8, 0, 0, 0, loc), SignedMinutes: -150},
		{ScoredAt: time.Date(2026, 6, 22, 8, 0, 0, 0, loc), SignedMinutes: -15},
	}
	settlements := []Settlement{
		{At: time.Date(2026, 6, 20, 12, 0, 0, 0, loc), Total: -90, Partial: true},
		{At: time.Date(2026, 6, 21, 18, 0, 0, 0, loc), Total: -60},
	}
	if got := OpenBalance(days, settlements); got != -15 {
		t.Fatalf("OpenBalance() = %d, want -15", got)
	}
}

func TestClearAmount_TakesHoursAndMinutesTowardZero(t *testing.T) {
	signed, full, err := ClearAmount(-150, 1, 30)
	if err != nil {
		t.Fatal(err)
	}
	if signed != -90 || full {
		t.Fatalf("ClearAmount() = %d full=%v, want -90 partial", signed, full)
	}
	signed, full, err = ClearAmount(150, 1, 30)
	if err != nil {
		t.Fatal(err)
	}
	if signed != 90 || full {
		t.Fatalf("ClearAmount() = %d full=%v, want +90 partial", signed, full)
	}
}

func TestClearAmount_ExactBalanceIsAFullClear(t *testing.T) {
	signed, full, err := ClearAmount(-60, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if signed != -60 || !full {
		t.Fatalf("ClearAmount() = %d full=%v, want -60 full", signed, full)
	}
}

func TestClearAmount_RejectsZeroOverClearAndBadClock(t *testing.T) {
	if _, _, err := ClearAmount(-150, 0, 0); err == nil {
		t.Fatal("expected zero minutes to be rejected")
	}
	if _, _, err := ClearAmount(0, 1, 0); err == nil {
		t.Fatal("expected a zero aggregate to be rejected")
	}
	if _, _, err := ClearAmount(-150, 3, 0); err == nil {
		t.Fatal("expected more than the open total to be rejected")
	}
	if _, _, err := ClearAmount(-150, 1, 60); err == nil {
		t.Fatal("expected minutes above 59 to be rejected")
	}
	if _, _, err := ClearAmount(-150, -1, 0); err == nil {
		t.Fatal("expected negative hours to be rejected")
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
