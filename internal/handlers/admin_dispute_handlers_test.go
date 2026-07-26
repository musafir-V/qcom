package handlers

import (
	"net/http"
	"net/http/httptest"
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

func TestStoreFilterFrom(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"absent means all stores", "/admin/disputes?status=OPEN", ""},
		{"empty means all stores", "/admin/disputes?status=OPEN&store_id=", ""},
		{"whitespace means all stores", "/admin/disputes?status=OPEN&store_id=%20%20", ""},
		{"numeric store", "/admin/disputes?status=OPEN&store_id=42", "42"},
		{"store is trimmed", "/admin/disputes?status=OPEN&store_id=%2042%20", "42"},
		{"unknown sentinel passes through", "/admin/disputes?status=OPEN&store_id=UNKNOWN", "UNKNOWN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			if got := storeFilterFrom(req); got != tc.want {
				t.Errorf("storeFilterFrom(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}
