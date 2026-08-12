package models

import (
	"time"

	"github.com/qcom/qcom/internal/timezone"
)

// ElectionDayClosureDate is the Africa/Lusaka calendar day when the store is
// manually closed for the full day (independent of opens_at/closes_at).
const ElectionDayClosureDate = "2026-08-13"

// IsElectionDayClosure reports whether t falls on the configured full-day closure
// date, evaluated in Africa/Lusaka.
func IsElectionDayClosure(t time.Time) bool {
	local := t.In(timezone.ZambiaLocation())
	return local.Format("2006-01-02") == ElectionDayClosureDate
}

// NextOpensAtAfterClosureDay returns the first scheduled opening after a full-day
// closure on closureDate: the following calendar day at opens_at (Africa/Lusaka).
func (d *Darkstore) NextOpensAtAfterClosureDay(closureDate string) (string, bool) {
	open, _, ok := d.openCloseMinutes()
	if !ok {
		return "", false
	}
	loc := timezone.ZambiaLocation()
	day, err := time.ParseInLocation("2006-01-02", closureDate, loc)
	if err != nil {
		return "", false
	}
	next := time.Date(day.Year(), day.Month(), day.Day(), open/60, open%60, 0, 0, loc).AddDate(0, 0, 1)
	return next.Format(time.RFC3339), true
}
