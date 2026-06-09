package timezone

import "time"

var zambiaLoc = mustLoad()

func mustLoad() *time.Location {
	loc, err := time.LoadLocation("Africa/Lusaka")
	if err != nil {
		loc = time.FixedZone("CAT", 2*60*60)
	}
	return loc
}

// ZambiaLocation returns the Africa/Lusaka *time.Location (UTC+2).
// Use this for ALL business-logic date boundaries in this codebase.
func ZambiaLocation() *time.Location { return zambiaLoc }

// Now returns the current time in Zambia timezone.
func Now() time.Time { return time.Now().In(zambiaLoc) }

// DateString returns today's date as "2006-01-02" in Zambia timezone.
func DateString() string { return Now().Format("2006-01-02") }

// StartOfDayString returns midnight (00:00:00) of the current Zambia day,
// formatted as RFC3339. Use as the "after" boundary for today's earnings,
// matching the RFC3339 created_at format written to the earnings ledger.
func StartOfDayString() string {
	n := Now()
	start := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, zambiaLoc)
	return start.Format(time.RFC3339)
}

// WeekStart returns the Monday of the current week in Zambia timezone,
// formatted as "2006-01-02".
func WeekStart() string {
	now := Now()
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7 // Sunday = 7 in ISO week
	}
	monday := now.AddDate(0, 0, -(weekday - 1))
	return monday.Format("2006-01-02")
}
