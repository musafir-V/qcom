package punctuality

import (
	"fmt"
	"time"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/timezone"
)

// SignedMinutes is the shift start minus the scan time, in minutes.
// Positive is early. Negative is late. Zero is on time.
func SignedMinutes(start, scan string) (int, error) {
	startMin, err := minutesOfDay(start)
	if err != nil {
		return 0, fmt.Errorf("shift start: %w", err)
	}
	scanMin, err := minutesOfDay(scan)
	if err != nil {
		return 0, fmt.Errorf("scan: %w", err)
	}
	return startMin - scanMin, nil
}

// DayScore is the frozen punctuality result for one Zambia day.
type DayScore struct {
	Date          string
	ShiftStart    string
	FirstScan     string
	SignedMinutes int
}

// ShiftStart is one expected arrival time and when it was saved.
type ShiftStart struct {
	Time  string
	SetAt time.Time
}

// ScoreInput is the shift starts in effect and the scan being considered.
type ScoreInput struct {
	Starts          []ShiftStart
	ShiftStart      string
	ShiftStartSetAt time.Time
	ScanAt          time.Time
	Reason          string
	AlreadyScored   bool
}

// ScoreScan reports the day score for a store-QR scan. ok is false when this
// scan does not open a scored day.
func ScoreScan(in ScoreInput) (DayScore, bool) {
	if in.AlreadyScored || in.Reason != string(models.ReasonScanStart) {
		return DayScore{}, false
	}
	start, ok := chooseStart(candidateStarts(in), in.ScanAt)
	if !ok {
		return DayScore{}, false
	}
	local := in.ScanAt.In(timezone.ZambiaLocation())
	hhmm := local.Format("15:04")
	signed, err := SignedMinutes(start, hhmm)
	if err != nil {
		return DayScore{}, false
	}
	return DayScore{
		Date:          local.Format("2006-01-02"),
		ShiftStart:    start,
		FirstScan:     hhmm,
		SignedMinutes: signed,
	}, true
}

func candidateStarts(in ScoreInput) []ShiftStart {
	if len(in.Starts) > 0 {
		return in.Starts
	}
	if in.ShiftStart == "" || in.ShiftStartSetAt.IsZero() {
		return nil
	}
	return []ShiftStart{{Time: in.ShiftStart, SetAt: in.ShiftStartSetAt}}
}

// chooseStart picks the saved start closest to the scan on that Zambia day.
// Distance does not wrap past midnight. An equal distance uses the earlier start.
// A start saved at or after the scan is ignored.
func chooseStart(starts []ShiftStart, scanAt time.Time) (string, bool) {
	local := scanAt.In(timezone.ZambiaLocation())
	scanMin := local.Hour()*60 + local.Minute()
	best := ""
	bestDist := 0
	found := false
	for _, start := range starts {
		if start.SetAt.IsZero() || scanAt.Before(start.SetAt) {
			continue
		}
		startMin, err := minutesOfDay(start.Time)
		if err != nil {
			continue
		}
		dist := startMin - scanMin
		if dist < 0 {
			dist = -dist
		}
		if !found || dist < bestDist || (dist == bestDist && start.Time < best) {
			found = true
			best = start.Time
			bestDist = dist
		}
	}
	return best, found
}

func minutesOfDay(hhmm string) (int, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return 0, fmt.Errorf("want HH:MM, got %q", hhmm)
	}
	return t.Hour()*60 + t.Minute(), nil
}
