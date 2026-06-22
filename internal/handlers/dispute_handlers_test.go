// internal/handlers/dispute_handlers_test.go
package handlers

import (
	"net/http"
	"testing"

	"github.com/qcom/qcom/internal/service"
)

func TestClassifyDisputeError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"order not found", service.ErrOrderNotFound, http.StatusNotFound, "ORDER_NOT_FOUND"},
		{"not disputable", service.ErrOrderNotDisputable, http.StatusConflict, "ORDER_NOT_DISPUTABLE"},
		{"unknown disposition", service.ErrDispositionNotFound, http.StatusBadRequest, "DISPOSITION_NOT_FOUND"},
		{"description required", service.ErrDescriptionRequired, http.StatusBadRequest, "DESCRIPTION_REQUIRED"},
		{"too many photos", service.ErrTooManyPhotos, http.StatusBadRequest, "TOO_MANY_PHOTOS"},
		{"invalid photo key", service.ErrInvalidPhotoKey, http.StatusBadRequest, "INVALID_PHOTO_KEY"},
		{"already open", service.ErrDisputeAlreadyOpen, http.StatusConflict, "DISPUTE_ALREADY_OPEN"},
		{"dispute not found", service.ErrDisputeNotFound, http.StatusNotFound, "DISPUTE_NOT_FOUND"},
		{"forbidden", service.ErrDisputeForbidden, http.StatusForbidden, "FORBIDDEN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code := classifyDisputeError(tc.err)
			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d", status, tc.wantStatus)
			}
			if code != tc.wantCode {
				t.Errorf("code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}
