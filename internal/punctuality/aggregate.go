package punctuality

import (
	"errors"
	"strings"
	"time"
)

// ErrClearZero is returned when the open aggregate is exactly zero.
var ErrClearZero = errors.New("open aggregate is zero")

// ErrClearNote is returned when a clear has no note.
var ErrClearNote = errors.New("clear note is required")

// ErrClearTooMuch is returned when the typed amount exceeds the open aggregate.
var ErrClearTooMuch = errors.New("clear amount is larger than the open aggregate")

// ErrClearAmount is returned when hours or minutes are not a clock amount.
var ErrClearAmount = errors.New("clear amount must be hours and minutes")

// ScoredDay is one frozen punctuality day.
type ScoredDay struct {
	ScoredAt      time.Time
	SignedMinutes int
}

// Settlement is one reduction of the open aggregate. Partial is false for a
// clear that marks days at or before At as settled, including clears stored
// before partial amounts existed.
type Settlement struct {
	At      time.Time
	Total   int
	Partial bool
}

// OpenBalance is the open aggregate after full clears and later partial clears.
func OpenBalance(days []ScoredDay, settlements []Settlement) int {
	clearedAt := time.Time{}
	for _, settlement := range settlements {
		if !settlement.Partial && settlement.At.After(clearedAt) {
			clearedAt = settlement.At
		}
	}
	total := OpenTotal(days, clearedAt)
	for _, settlement := range settlements {
		if !settlement.Partial || (!clearedAt.IsZero() && !settlement.At.After(clearedAt)) {
			continue
		}
		total -= settlement.Total
	}
	return total
}

// OpenTotal sums scored days strictly after clearedAt. The zero time includes
// every day.
func OpenTotal(days []ScoredDay, clearedAt time.Time) int {
	total := 0
	for _, day := range days {
		if !clearedAt.IsZero() && !day.ScoredAt.After(clearedAt) {
			continue
		}
		total += day.SignedMinutes
	}
	return total
}

// ClearAmount converts hours and minutes into a signed settlement toward zero.
// An amount equal to the open total is a full clear.
func ClearAmount(open, hours, minutes int) (signed int, full bool, err error) {
	if hours < 0 || minutes < 0 || minutes > 59 {
		return 0, false, ErrClearAmount
	}
	amount := hours*60 + minutes
	if open == 0 || amount == 0 {
		return 0, false, ErrClearZero
	}
	openAbs := open
	if openAbs < 0 {
		openAbs = -openAbs
	}
	if amount > openAbs {
		return 0, false, ErrClearTooMuch
	}
	signed = amount
	if open < 0 {
		signed = -amount
	}
	return signed, signed == open, nil
}

// ValidateClear allows a non-zero total with a note. The sign may be early or late.
func ValidateClear(total int, note string) error {
	if total == 0 {
		return ErrClearZero
	}
	if strings.TrimSpace(note) == "" {
		return ErrClearNote
	}
	return nil
}
