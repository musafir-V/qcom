package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/qcom/qcom/internal/service"
)

func TestClassifyTaskUpdateError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "trip not found",
			err:        fmt.Errorf("%w: trip-123", service.ErrTripNotFound),
			wantStatus: http.StatusNotFound,
			wantCode:   "NOT_FOUND",
		},
		{
			name:       "task not found",
			err:        fmt.Errorf("%w: task-456", service.ErrTaskNotFound),
			wantStatus: http.StatusNotFound,
			wantCode:   "NOT_FOUND",
		},
		{
			name:       "forbidden",
			err:        service.ErrTripForbidden,
			wantStatus: http.StatusForbidden,
			wantCode:   "FORBIDDEN",
		},
		{
			name:       "trip closed",
			err:        fmt.Errorf("%w: completed", service.ErrTripClosed),
			wantStatus: http.StatusBadRequest,
			wantCode:   "TRIP_ALREADY_CLOSED",
		},
		{
			name:       "prerequisite incomplete",
			err:        fmt.Errorf("%w: %w", service.ErrPrerequisiteIncomplete, errors.New("pickup not done")),
			wantStatus: http.StatusBadRequest,
			wantCode:   "PREREQUISITE_TASK_INCOMPLETE",
		},
		{
			name:       "invalid otp",
			err:        fmt.Errorf("%w", service.ErrInvalidOTP),
			wantStatus: http.StatusBadRequest,
			wantCode:   "INVALID_OTP",
		},
		{
			name:       "invalid transition",
			err:        fmt.Errorf("%w: created -> completed", service.ErrInvalidTransition),
			wantStatus: http.StatusBadRequest,
			wantCode:   "INVALID_TASK_TRANSITION",
		},
		{
			name:       "order not ready for pickup",
			err:        service.ErrOrderNotReadyForPickup,
			wantStatus: http.StatusConflict,
			wantCode:   "ORDER_NOT_READY",
		},
		{
			name:       "unknown error defaults to 500",
			err:        errors.New("dynamodb timeout"),
			wantStatus: http.StatusInternalServerError,
			wantCode:   "UPDATE_FAILED",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code := classifyTaskUpdateError(tc.err)
			if status != tc.wantStatus {
				t.Errorf("status: got %d, want %d", status, tc.wantStatus)
			}
			if code != tc.wantCode {
				t.Errorf("code: got %q, want %q", code, tc.wantCode)
			}
		})
	}
}

func TestClassifyAcceptRejectError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"not found", fmt.Errorf("%w: t-1", service.ErrTripNotFound), http.StatusNotFound, "NOT_FOUND"},
		{"forbidden", service.ErrTripForbidden, http.StatusForbidden, "FORBIDDEN"},
		{"invalid state", fmt.Errorf("%w: from accepted", service.ErrInvalidTripTransition), http.StatusConflict, "INVALID_TRIP_STATE"},
		{"unknown defaults 500", errors.New("boom"), http.StatusInternalServerError, "ACTION_FAILED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code := classifyAcceptRejectError(tc.err)
			if status != tc.wantStatus {
				t.Errorf("status: got %d, want %d", status, tc.wantStatus)
			}
			if code != tc.wantCode {
				t.Errorf("code: got %q, want %q", code, tc.wantCode)
			}
		})
	}
}
