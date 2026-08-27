package models

import (
	"regexp"
	"time"

	"github.com/qcom/qcom/internal/timezone"
)

const OperatingHoursTimezone = "Africa/Lusaka"

var hhmmPattern = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)

func parseHHMM(s string) (minutes int, ok bool) {
	m := hhmmPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	h := int(m[1][0]-'0')*10 + int(m[1][1]-'0')
	min := int(m[2][0]-'0')*10 + int(m[2][1]-'0')
	return h*60 + min, true
}

// ValidOperatingHours reports whether opens_at/closes_at are present, well-formed,
// and form a same-day window (closes_at must be after opens_at).
func (d *Darkstore) ValidOperatingHours() bool {
	open, ok1 := parseHHMM(d.OpensAt)
	close, ok2 := parseHHMM(d.ClosesAt)
	if !ok1 || !ok2 {
		return false
	}
	return close > open
}

func (d *Darkstore) minutesSinceMidnight(t time.Time) int {
	local := t.In(timezone.ZambiaLocation())
	return local.Hour()*60 + local.Minute()
}

func (d *Darkstore) openCloseMinutes() (open, close int, ok bool) {
	if !d.ValidOperatingHours() {
		return 0, 0, false
	}
	open, _ = parseHHMM(d.OpensAt)
	close, _ = parseHHMM(d.ClosesAt)
	return open, close, true
}

// IsOperationalAt reports whether the store is within its daily operating window
// at the given instant, evaluated in Africa/Lusaka.
func (d *Darkstore) IsOperationalAt(t time.Time) bool {
	open, close, ok := d.openCloseMinutes()
	if !ok {
		return false
	}
	now := d.minutesSinceMidnight(t)
	return now >= open && now < close
}

// ScannedSinceOpen reports whether lastScanAt (RFC3339) is at or after today's
// opens_at in Africa/Lusaka. Empty/unparseable lastScanAt or invalid hours
// fail closed (false). closes_at is not considered.
func (d *Darkstore) ScannedSinceOpen(lastScanAt string, now time.Time) bool {
	open, _, ok := d.openCloseMinutes()
	if !ok || lastScanAt == "" {
		return false
	}
	scanned, err := time.Parse(time.RFC3339, lastScanAt)
	if err != nil {
		return false
	}
	local := now.In(timezone.ZambiaLocation())
	opensToday := time.Date(
		local.Year(), local.Month(), local.Day(),
		open/60, open%60, 0, 0,
		timezone.ZambiaLocation(),
	)
	return !scanned.Before(opensToday)
}

// NextOpensAt returns the next scheduled opening as RFC3339 in Africa/Lusaka.
// The second return value is false when operating hours are invalid.
func (d *Darkstore) NextOpensAt(t time.Time) (string, bool) {
	open, _, ok := d.openCloseMinutes()
	if !ok {
		return "", false
	}

	local := t.In(timezone.ZambiaLocation())
	now := d.minutesSinceMidnight(t)
	openTime := time.Date(
		local.Year(), local.Month(), local.Day(),
		open/60, open%60, 0, 0,
		timezone.ZambiaLocation(),
	)
	if now >= open {
		openTime = openTime.AddDate(0, 0, 1)
	}
	return openTime.Format(time.RFC3339), true
}
