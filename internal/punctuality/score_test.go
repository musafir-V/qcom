package punctuality

import (
	"testing"
	"time"
)

func TestSignedMinutes_EarlyScanIsPositive(t *testing.T) {
	got, err := SignedMinutes("08:00", "07:40")
	if err != nil {
		t.Fatal(err)
	}
	if got != 20 {
		t.Fatalf("SignedMinutes() = %d, want 20", got)
	}
}

func TestSignedMinutes_LateScanIsNegative(t *testing.T) {
	got, err := SignedMinutes("08:00", "08:20")
	if err != nil {
		t.Fatal(err)
	}
	if got != -20 {
		t.Fatalf("SignedMinutes() = %d, want -20", got)
	}
}

func TestSignedMinutes_OnTimeIsZero(t *testing.T) {
	got, err := SignedMinutes("08:00", "08:00")
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("SignedMinutes() = %d, want 0", got)
	}
}

func TestSignedMinutes_RejectsBadClock(t *testing.T) {
	if _, err := SignedMinutes("8am", "08:00"); err == nil {
		t.Fatal("expected error for bad shift start")
	}
	if _, err := SignedMinutes("08:00", "25:00"); err == nil {
		t.Fatal("expected error for bad scan time")
	}
}

func TestScoreScan_FirstScanStartAfterShiftIsSaved(t *testing.T) {
	loc := time.FixedZone("CAT", 2*60*60)
	setAt := time.Date(2026, 6, 22, 6, 0, 0, 0, loc)
	scanAt := time.Date(2026, 6, 22, 7, 40, 0, 0, loc)

	got, ok := ScoreScan(ScoreInput{
		ShiftStart:      "08:00",
		ShiftStartSetAt: setAt,
		ScanAt:          scanAt,
		Reason:          "scan_start",
	})
	if !ok {
		t.Fatal("expected a day score")
	}
	if got.Date != "2026-06-22" || got.ShiftStart != "08:00" || got.FirstScan != "07:40" || got.SignedMinutes != 20 {
		t.Fatalf("score = %+v", got)
	}
}

func TestScoreScan_ReturnFromFreeScores(t *testing.T) {
	loc := time.FixedZone("CAT", 2*60*60)
	got, ok := ScoreScan(ScoreInput{
		ShiftStart:      "08:00",
		ShiftStartSetAt: time.Date(2026, 6, 22, 6, 0, 0, 0, loc),
		ScanAt:          time.Date(2026, 6, 22, 8, 13, 0, 0, loc),
		Reason:          "scan_return",
	})
	if !ok {
		t.Fatal("a return from free is that day's arrival")
	}
	if got.Date != "2026-06-22" || got.ShiftStart != "08:00" || got.FirstScan != "08:13" || got.SignedMinutes != -13 {
		t.Fatalf("score = %+v", got)
	}
}

func TestScoreScan_BusyReturnDoesNotScore(t *testing.T) {
	loc := time.FixedZone("CAT", 2*60*60)
	_, ok := ScoreScan(ScoreInput{
		ShiftStart:      "08:00",
		ShiftStartSetAt: time.Date(2026, 6, 22, 6, 0, 0, 0, loc),
		ScanAt:          time.Date(2026, 6, 22, 8, 13, 0, 0, loc),
		Reason:          "scan_return_while_busy",
	})
	if ok {
		t.Fatal("a scan while still on a delivery must not score")
	}
}

func TestScoreScan_NoShiftStartDoesNotScore(t *testing.T) {
	loc := time.FixedZone("CAT", 2*60*60)
	_, ok := ScoreScan(ScoreInput{
		ScanAt: time.Date(2026, 6, 22, 7, 40, 0, 0, loc),
		Reason: "scan_start",
	})
	if ok {
		t.Fatal("a scan with no shift start must not score")
	}
}

func TestScoreScan_ScanBeforeShiftWasSavedDoesNotScore(t *testing.T) {
	loc := time.FixedZone("CAT", 2*60*60)
	_, ok := ScoreScan(ScoreInput{
		ShiftStart:      "08:00",
		ShiftStartSetAt: time.Date(2026, 6, 22, 10, 0, 0, 0, loc),
		ScanAt:          time.Date(2026, 6, 22, 8, 20, 0, 0, loc),
		Reason:          "scan_start",
	})
	if ok {
		t.Fatal("a scan from before the shift start was saved must not score")
	}
}

func TestScoreScan_AlreadyScoredDayStaysFrozen(t *testing.T) {
	loc := time.FixedZone("CAT", 2*60*60)
	_, ok := ScoreScan(ScoreInput{
		ShiftStart:      "09:00",
		ShiftStartSetAt: time.Date(2026, 6, 22, 10, 0, 0, 0, loc),
		ScanAt:          time.Date(2026, 6, 22, 10, 5, 0, 0, loc),
		Reason:          "scan_start",
		AlreadyScored:   true,
	})
	if ok {
		t.Fatal("a day that already has a score must not be rewritten")
	}
}

func TestScoreScan_UsesClosestStartSavedBeforeTheScan(t *testing.T) {
	loc := time.FixedZone("CAT", 2*60*60)
	saved := time.Date(2026, 6, 22, 6, 0, 0, 0, loc)
	got, ok := ScoreScan(ScoreInput{
		Starts: []ShiftStart{
			{Time: "08:00", SetAt: saved},
			{Time: "16:00", SetAt: saved},
		},
		ScanAt: time.Date(2026, 6, 22, 16, 5, 0, 0, loc),
		Reason: "scan_start",
	})
	if !ok {
		t.Fatal("expected a day score")
	}
	if got.ShiftStart != "16:00" || got.SignedMinutes != -5 || got.FirstScan != "16:05" {
		t.Fatalf("score = %+v", got)
	}
}

func TestScoreScan_EqualDistanceUsesEarlierStart(t *testing.T) {
	loc := time.FixedZone("CAT", 2*60*60)
	saved := time.Date(2026, 6, 22, 6, 0, 0, 0, loc)
	got, ok := ScoreScan(ScoreInput{
		Starts: []ShiftStart{
			{Time: "16:00", SetAt: saved},
			{Time: "08:00", SetAt: saved},
		},
		ScanAt: time.Date(2026, 6, 22, 12, 0, 0, 0, loc),
		Reason: "scan_start",
	})
	if !ok {
		t.Fatal("expected a day score")
	}
	if got.ShiftStart != "08:00" || got.SignedMinutes != -240 {
		t.Fatalf("score = %+v", got)
	}
}

func TestScoreScan_ClosestDoesNotWrapPastMidnight(t *testing.T) {
	loc := time.FixedZone("CAT", 2*60*60)
	saved := time.Date(2026, 6, 22, 0, 30, 0, 0, loc)
	got, ok := ScoreScan(ScoreInput{
		Starts: []ShiftStart{
			{Time: "22:00", SetAt: saved},
			{Time: "06:00", SetAt: saved},
		},
		ScanAt: time.Date(2026, 6, 22, 1, 0, 0, 0, loc),
		Reason: "scan_start",
	})
	if !ok {
		t.Fatal("expected a day score")
	}
	if got.ShiftStart != "06:00" || got.SignedMinutes != 300 {
		t.Fatalf("score = %+v", got)
	}
}

func TestScoreScan_IgnoresStartSavedAfterTheScan(t *testing.T) {
	loc := time.FixedZone("CAT", 2*60*60)
	got, ok := ScoreScan(ScoreInput{
		Starts: []ShiftStart{
			{Time: "08:00", SetAt: time.Date(2026, 6, 22, 6, 0, 0, 0, loc)},
			{Time: "16:00", SetAt: time.Date(2026, 6, 22, 17, 0, 0, 0, loc)},
		},
		ScanAt: time.Date(2026, 6, 22, 16, 5, 0, 0, loc),
		Reason: "scan_start",
	})
	if !ok {
		t.Fatal("expected a day score")
	}
	if got.ShiftStart != "08:00" || got.SignedMinutes != -485 {
		t.Fatalf("score = %+v", got)
	}
}
