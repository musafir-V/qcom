package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/qcom/qcom/internal/service"
)

func TestClassifyAdminAssignError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"trip not found", fmt.Errorf("%w: o-1", service.ErrTripNotFound), http.StatusNotFound, "ORDER_NOT_FOUND"},
		{"not assignable", fmt.Errorf("%w: assigned", service.ErrInvalidTripTransition), http.StatusConflict, "ORDER_NOT_ASSIGNABLE"},
		{"de not found", service.ErrDENotFound, http.StatusNotFound, "DRIVER_NOT_FOUND"},
		{"de not eligible", service.ErrDENotEligible, http.StatusConflict, "DRIVER_NOT_ELIGIBLE"},
		{"unknown 500", errors.New("boom"), http.StatusInternalServerError, "ASSIGN_FAILED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code := classifyAdminAssignError(tc.err)
			if status != tc.wantStatus {
				t.Errorf("status: got %d, want %d", status, tc.wantStatus)
			}
			if code != tc.wantCode {
				t.Errorf("code: got %q, want %q", code, tc.wantCode)
			}
		})
	}
}

func TestClassifyReassignError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"trip missing", service.ErrTripNotFound, http.StatusNotFound, "TRIP_NOT_FOUND"},
		{"driver missing", service.ErrDENotFound, http.StatusNotFound, "DRIVER_NOT_FOUND"},
		{"bad status", service.ErrTripNotReassignable, http.StatusConflict, "TRIP_NOT_REASSIGNABLE"},
		{"driver busy", service.ErrDENotEligible, http.StatusConflict, "DRIVER_NOT_ELIGIBLE"},
		{"wrong store", service.ErrDriverWrongStore, http.StatusConflict, "DRIVER_WRONG_STORE"},
		{"same driver", service.ErrSameDriver, http.StatusBadRequest, "SAME_DRIVER"},
		{"bad reason", service.ErrInvalidReasonCode, http.StatusBadRequest, "INVALID_REASON_CODE"},
		{"reassign conflict", service.ErrReassignConflict, http.StatusConflict, "REASSIGN_CONFLICT"},
		{"unknown", errors.New("boom"), http.StatusInternalServerError, "REASSIGN_FAILED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code := classifyReassignError(tc.err)
			if status != tc.wantStatus || code != tc.wantCode {
				t.Fatalf("got (%d, %q), want (%d, %q)", status, code, tc.wantStatus, tc.wantCode)
			}
		})
	}
}

// Wrapped errors must classify the same as bare ones — the service wraps every
// sentinel with %w plus context, so classification must use errors.Is, not ==.
func TestClassifyReassignError_Wrapped(t *testing.T) {
	wrapped := fmt.Errorf("%w: DE-A", service.ErrSameDriver)
	status, code := classifyReassignError(wrapped)
	if status != http.StatusBadRequest || code != "SAME_DRIVER" {
		t.Fatalf("got (%d, %q), want (400, SAME_DRIVER)", status, code)
	}
}

func TestTruncateRunes_UnderLimit(t *testing.T) {
	s := "chain snapped"
	if got := truncateRunes(s, reassignNoteMaxRunes); got != s {
		t.Fatalf("expected unchanged string, got %q", got)
	}
}

func TestTruncateRunes_OverLimit(t *testing.T) {
	s := strings.Repeat("a", 600)
	got := truncateRunes(s, reassignNoteMaxRunes)
	if len(got) != reassignNoteMaxRunes {
		t.Fatalf("expected %d chars, got %d", reassignNoteMaxRunes, len(got))
	}
}

// Truncation must operate on runes, not bytes, so a multi-byte character
// (e.g. an emoji or accented letter) at the boundary is never split into an
// invalid UTF-8 fragment.
func TestTruncateRunes_MultiByteNotSplit(t *testing.T) {
	// 499 ASCII runes + one 3-byte rune ('€') = 500 runes total, well past a
	// byte-oriented truncation point if one were used at 500 bytes.
	s := strings.Repeat("a", 499) + "€" + strings.Repeat("b", 50)
	got := truncateRunes(s, reassignNoteMaxRunes)
	runes := []rune(got)
	if len(runes) != reassignNoteMaxRunes {
		t.Fatalf("expected %d runes, got %d (%q)", reassignNoteMaxRunes, len(runes), got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncated string is not valid UTF-8: %q", got)
	}
	if runes[499] != '€' {
		t.Fatalf("expected last rune to be the untouched multi-byte character, got %q", runes[499])
	}
}
