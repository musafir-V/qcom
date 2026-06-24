package handlers

import (
	"net/http"
	"testing"

	"github.com/qcom/qcom/internal/service"
)

func TestClassifyAdminDisputeError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"invalid status", service.ErrInvalidDisputeStatus, http.StatusBadRequest, "INVALID_STATUS"},
		{"invalid transition", service.ErrInvalidDisputeTransition, http.StatusConflict, "INVALID_TRANSITION"},
		{"note required", service.ErrResolutionNoteRequired, http.StatusBadRequest, "RESOLUTION_NOTE_REQUIRED"},
		{"not found", service.ErrDisputeNotFound, http.StatusNotFound, "DISPUTE_NOT_FOUND"},
		{"other", errAdminTestSentinel, http.StatusInternalServerError, "INTERNAL_ERROR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code := classifyAdminDisputeError(tc.err)
			if status != tc.wantStatus || code != tc.wantCode {
				t.Errorf("got (%d,%q) want (%d,%q)", status, code, tc.wantStatus, tc.wantCode)
			}
		})
	}
}

var errAdminTestSentinel = &sentinelErr{}

type sentinelErr struct{}

func (*sentinelErr) Error() string { return "boom" }
