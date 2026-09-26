package service

import (
	"errors"
	"time"
)

// ErrSessionAbsoluteExpired is returned when a refresh family has passed its
// absolute deadline and must not be minted further.
var ErrSessionAbsoluteExpired = errors.New("session absolute expired")

// SessionWindow is the idle-plus-absolute lifetime of a refresh-token family.
// Idle slides on each successful refresh. Absolute is fixed at first login.
type SessionWindow struct {
	Idle     time.Duration // slides on each successful refresh
	Absolute time.Time     // fixed at first login in the family, never slides
}

// RefreshExpiresAt returns the refresh JWT exp.
// ok is false when now is at or after Absolute (the family is finished).
func (w SessionWindow) RefreshExpiresAt(now time.Time) (exp time.Time, ok bool) {
	if !w.Absolute.IsZero() && !now.Before(w.Absolute) {
		return time.Time{}, false
	}
	exp = now.Add(w.Idle)
	if !w.Absolute.IsZero() && exp.After(w.Absolute) {
		exp = w.Absolute
	}
	return exp, true
}
