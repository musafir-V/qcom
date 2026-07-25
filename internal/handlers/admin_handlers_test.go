package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

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
