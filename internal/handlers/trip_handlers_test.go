package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
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
			name:       "drop not reached",
			err:        fmt.Errorf("%w", service.ErrDropNotReached),
			wantStatus: http.StatusBadRequest,
			wantCode:   "DROP_NOT_REACHED",
		},
		{
			name:       "missing location",
			err:        fmt.Errorf("%w", service.ErrMissingLocation),
			wantStatus: http.StatusBadRequest,
			wantCode:   "MISSING_LOCATION",
		},
		{
			name:       "invalid coordinates",
			err:        fmt.Errorf("%w", service.ErrInvalidCoordinates),
			wantStatus: http.StatusBadRequest,
			wantCode:   "INVALID_COORDINATES",
		},
		{
			name:       "order not deliverable",
			err:        fmt.Errorf("%w", service.ErrOrderNotDeliverable),
			wantStatus: http.StatusConflict,
			wantCode:   "ORDER_NOT_DELIVERABLE",
		},
		{
			name:       "java order cancelled",
			err:        fmt.Errorf("%w", service.ErrJavaOrderCancelled),
			wantStatus: http.StatusConflict,
			wantCode:   "ORDER_CANCELLED",
		},
		{
			name:       "rider required",
			err:        fmt.Errorf("%w", service.ErrRiderRequired),
			wantStatus: http.StatusBadRequest,
			wantCode:   "RIDER_REQUIRED",
		},
		{
			name:       "rider busy elsewhere",
			err:        fmt.Errorf("%w", service.ErrRiderBusyElsewhere),
			wantStatus: http.StatusConflict,
			wantCode:   "RIDER_BUSY_ELSEWHERE",
		},
		{
			name:       "already delivered",
			err:        fmt.Errorf("%w", service.ErrAlreadyDelivered),
			wantStatus: http.StatusConflict,
			wantCode:   "ALREADY_DELIVERED",
		},
		{
			name:       "driver not found",
			err:        fmt.Errorf("%w: +260770000000", service.ErrDENotFound),
			wantStatus: http.StatusNotFound,
			wantCode:   "DRIVER_NOT_FOUND",
		},
		{
			name:       "force-assign conflict",
			err:        fmt.Errorf("%w: admin assign conflict", service.ErrForceAssignConflict),
			wantStatus: http.StatusConflict,
			wantCode:   "FORCE_ASSIGN_CONFLICT",
		},
		{
			name:       "order not packed",
			err:        fmt.Errorf("%w", service.ErrOrderNotPacked),
			wantStatus: http.StatusConflict,
			wantCode:   "ORDER_NOT_PACKED",
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

func TestClassifyVerifyPickupError_OrderNotPacked(t *testing.T) {
	status, code := classifyVerifyPickupError(fmt.Errorf("%w", service.ErrOrderNotPacked))
	if status != http.StatusConflict {
		t.Errorf("status: got %d, want %d", status, http.StatusConflict)
	}
	if code != "ORDER_NOT_PACKED" {
		t.Errorf("code: got %q, want %q", code, "ORDER_NOT_PACKED")
	}
}

func TestEditTripByOrder_MissingOrderID(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	h := &TripHandlers{logger: logger}

	req := httptest.NewRequest(http.MethodPost, "/internal/v1/trips/edit-by-order", strings.NewReader(`{
		"payment_method": "COD",
		"grand_total": 100,
		"currency": "ZMW",
		"delivery_zone": "Blue Rack 2",
		"items": [{"sku": "SKU-1", "name": "Milk", "quantity": 1}]
	}`))
	rec := httptest.NewRecorder()
	h.EditTripByOrder(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
	var body ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "MISSING_FIELD" {
		t.Fatalf("code: got %q, want MISSING_FIELD", body.Error.Code)
	}
}
