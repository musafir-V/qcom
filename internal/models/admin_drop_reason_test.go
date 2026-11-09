package models

import (
	"errors"
	"strings"
	"testing"
)

func TestAdminDropReasonKeys(t *testing.T) {
	r := AdminDropReason{OrderID: "ORD-1"}
	if got, want := r.GetPK(), "ADMINDROP!ORD-1"; got != want {
		t.Fatalf("GetPK() = %q, want %q", got, want)
	}
	if got, want := r.GetSK(), "REASON"; got != want {
		t.Fatalf("GetSK() = %q, want %q", got, want)
	}
}

func TestFormatAdminDropReason(t *testing.T) {
	cases := []struct {
		code, note, want string
		err              error
	}{
		{AdminDropReasonOTPNotProvided, "", "OTP Not Provided", nil},
		{AdminDropReasonInternalPurchase, "ignored", "Internal Purchase", nil},
		{AdminDropReasonBatchedOrder, "", "Batched Order", nil},
		{AdminDropReasonNotListed, "  customer left it  ", "Not Listed: customer left it", nil},
		{"", "", "", ErrInvalidAdminDropReason},
		{"bogus", "", "", ErrInvalidAdminDropReason},
		{AdminDropReasonNotListed, "   ", "", ErrAdminDropReasonNoteRequired},
	}
	for _, tc := range cases {
		got, err := FormatAdminDropReason(tc.code, tc.note)
		if tc.err != nil {
			if !errors.Is(err, tc.err) {
				t.Fatalf("code=%q note=%q: err=%v, want %v", tc.code, tc.note, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("code=%q: unexpected err %v", tc.code, err)
		}
		if got != tc.want {
			t.Fatalf("code=%q: got %q, want %q", tc.code, got, tc.want)
		}
	}

	long := strings.Repeat("x", AdminDropReasonNoteMaxRunes+1)
	if _, err := FormatAdminDropReason(AdminDropReasonNotListed, long); !errors.Is(err, ErrAdminDropReasonNoteTooLong) {
		t.Fatalf("too-long note: err=%v", err)
	}
}
