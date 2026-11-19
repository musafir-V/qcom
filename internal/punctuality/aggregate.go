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

// ScoredDay is one frozen punctuality day.
type ScoredDay struct {
	ScoredAt      time.Time
	SignedMinutes int
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
