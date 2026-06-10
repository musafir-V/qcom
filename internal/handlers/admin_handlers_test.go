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
