package timezone

import (
	"testing"
	"time"
)

// ZambiaLocation must represent UTC+2 (Africa/Lusaka has no DST).
func TestZambiaLocation_IsUTCPlus2(t *testing.T) {
	loc := ZambiaLocation()
	if loc == nil {
		t.Fatal("ZambiaLocation returned nil")
	}

	// Pick an arbitrary instant; offset must be +2h regardless of date.
	ref := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	_, offset := ref.In(loc).Zone()
	if offset != 2*60*60 {
		t.Errorf("expected offset 7200s (UTC+2), got %d", offset)
	}
}

// DateString must return today's date in Zambia time as "2006-01-02".
func TestDateString_MatchesZambiaToday(t *testing.T) {
	want := time.Now().In(ZambiaLocation()).Format("2006-01-02")
	got := DateString()
	if got != want {
		t.Errorf("DateString() = %q, want %q", got, want)
	}
}

// WeekStart must return the Monday of the current week (Zambia time).
func TestWeekStart_IsMonday(t *testing.T) {
	ws := WeekStart()

	parsed, err := time.ParseInLocation("2006-01-02", ws, ZambiaLocation())
	if err != nil {
		t.Fatalf("WeekStart() = %q is not a valid date: %v", ws, err)
	}

	if parsed.Weekday() != time.Monday {
		t.Errorf("WeekStart() = %q is a %s, want Monday", ws, parsed.Weekday())
	}
}

// WeekStart's Monday must be on-or-before today and within the last 7 days.
func TestWeekStart_WithinCurrentWeek(t *testing.T) {
	loc := ZambiaLocation()
	ws := WeekStart()
	monday, err := time.ParseInLocation("2006-01-02", ws, loc)
	if err != nil {
		t.Fatalf("WeekStart() = %q is not a valid date: %v", ws, err)
	}

	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	if monday.After(today) {
		t.Errorf("WeekStart() = %q is after today %q", ws, today.Format("2006-01-02"))
	}
	if today.Sub(monday) >= 7*24*time.Hour {
		t.Errorf("WeekStart() = %q is more than 6 days before today %q", ws, today.Format("2006-01-02"))
	}
}
