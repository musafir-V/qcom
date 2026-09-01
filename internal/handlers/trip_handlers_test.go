package handlers

import (
	"context"
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

type stubEditTripByOrder struct {
	result service.PaymentUpdateResult
}

func (s *stubEditTripByOrder) EditTripByOrder(_ context.Context, _ service.EditTripByOrderInput) (service.PaymentUpdateResult, error) {
	return s.result, nil
}

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
	rec := postEditTripByOrder(t, `{
		"payment_method": "COD",
		"grand_total": 100,
		"currency": "ZMW",
		"delivery_zone": "Blue Rack 2",
		"items": [{"sku": "SKU-1", "name": "Milk", "quantity": 1}]
	}`)

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

func TestEditTripByOrder_OmittedItemsRejected(t *testing.T) {
	rec := postEditTripByOrder(t, `{
		"order_id": "ORD-1",
		"payment_method": "COD",
		"grand_total": 100
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("omitted items must 400, got %d body %s", rec.Code, rec.Body.String())
	}
	var body ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "MISSING_FIELD" {
		t.Fatalf("code: got %q, want MISSING_FIELD", body.Error.Code)
	}
	if !strings.Contains(body.Error.Message, "items") {
		t.Fatalf("message %q must name items", body.Error.Message)
	}
}

func TestEditTripByOrder_EmptyItemsListAllowed(t *testing.T) {
	rec := postEditTripByOrder(t, `{
		"order_id": "ORD-1",
		"payment_method": "COD",
		"grand_total": 100,
		"items": []
	}`)
	assertEditByOrderSucceeded(t, rec, "items:[]")
}

func TestEditTripByOrder_OmittedPaymentMethodRejected(t *testing.T) {
	rec := postEditTripByOrder(t, `{
		"order_id": "ORD-1",
		"grand_total": 100,
		"items": [{"sku": "SKU-1", "name": "Milk", "quantity": 1}]
	}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("omitted payment_method must 400, got %d body %s", rec.Code, rec.Body.String())
	}
	var body ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "MISSING_FIELD" {
		t.Fatalf("code: got %q, want MISSING_FIELD", body.Error.Code)
	}
	if !strings.Contains(body.Error.Message, "payment_method") {
		t.Fatalf("message %q must name payment_method", body.Error.Message)
	}
}

func TestEditTripByOrder_EmptyPaymentMethodAllowed(t *testing.T) {
	rec := postEditTripByOrder(t, `{
		"order_id": "ORD-1",
		"payment_method": "",
		"grand_total": 100,
		"items": [{"sku": "SKU-1", "name": "Milk", "quantity": 1}]
	}`)
	assertEditByOrderSucceeded(t, rec, "payment_method:\"\"")
}

func TestEditTripByOrder_ZeroGrandTotalAllowed(t *testing.T) {
	rec := postEditTripByOrder(t, `{
		"order_id": "ORD-1",
		"payment_method": "COD",
		"grand_total": 0,
		"items": [{"sku": "SKU-1", "name": "Milk", "quantity": 1}]
	}`)
	assertEditByOrderSucceeded(t, rec, "grand_total 0")
}

func TestEditTripByOrder_ValidBodySucceeds(t *testing.T) {
	rec := postEditTripByOrder(t, `{
		"order_id": "ORD-1",
		"payment_method": "COD",
		"grand_total": 100,
		"currency": "ZMW",
		"delivery_zone": "Blue Rack 2",
		"items": [{"sku": "SKU-1", "name": "Milk", "quantity": 1}]
	}`)
	assertEditByOrderSucceeded(t, rec, "valid body")
}

type stubCompleteTripByOrder struct {
	result service.PaymentUpdateResult
	err    error
	got    service.CompleteByOrderInput
}

func (s *stubCompleteTripByOrder) CompleteByOrder(_ context.Context, in service.CompleteByOrderInput) (service.PaymentUpdateResult, error) {
	s.got = in
	return s.result, s.err
}

func TestCompleteTripByOrder_MissingOrderID(t *testing.T) {
	rec := postCompleteTripByOrder(t, `{"status":"OUT_FOR_DELIVERY"}`, service.PaymentUpdateResult{Updated: true}, nil)
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

func TestCompleteTripByOrder_TerminalIs200(t *testing.T) {
	rec := postCompleteTripByOrder(t, `{"order_id":"ORD-1","status":"DELIVERED"}`,
		service.PaymentUpdateResult{Updated: false, Reason: "trip_terminal"}, nil)
	if rec.Code == http.StatusConflict {
		t.Fatalf("trip_terminal must not 409 (Java would retry), got %d body %s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Updated bool   `json:"updated"`
		Reason  string `json:"reason"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Updated || got.Reason != "trip_terminal" {
		t.Fatalf("got %+v, want updated=false reason=trip_terminal", got)
	}
}

func TestCompleteTripByOrder_InvalidStatus400(t *testing.T) {
	rec := postCompleteTripByOrder(t, `{"order_id":"ORD-1","status":"PACKING"}`,
		service.PaymentUpdateResult{}, service.ErrInvalidStatus)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400, body %s", rec.Code, rec.Body.String())
	}
	var body ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "INVALID_STATUS" {
		t.Fatalf("code: got %q, want INVALID_STATUS", body.Error.Code)
	}
}

func TestCompleteTripByOrder_MissingStatus(t *testing.T) {
	rec := postCompleteTripByOrder(t, `{"order_id":"ORD-1"}`, service.PaymentUpdateResult{Updated: true}, nil)
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

func TestCompleteTripByOrder_InvalidJSON(t *testing.T) {
	rec := postCompleteTripByOrder(t, `{not-json`, service.PaymentUpdateResult{}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
	var body ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "INVALID_REQUEST" {
		t.Fatalf("code: got %q, want INVALID_REQUEST", body.Error.Code)
	}
}

func TestCompleteTripByOrder_ServiceError500(t *testing.T) {
	rec := postCompleteTripByOrder(t, `{"order_id":"ORD-1","status":"DELIVERED"}`,
		service.PaymentUpdateResult{}, errors.New("dynamo timeout"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want 500, body %s", rec.Code, rec.Body.String())
	}
}

func TestCompleteTripByOrder_DeliveredUpdatedTrue(t *testing.T) {
	rec := postCompleteTripByOrder(t, `{"order_id":"ORD-1","status":"DELIVERED"}`,
		service.PaymentUpdateResult{Updated: true}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Updated bool   `json:"updated"`
		Reason  string `json:"reason"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Updated {
		t.Fatalf("got %+v, want updated=true", got)
	}
}

func TestCompleteTripByOrder_NoTrip200(t *testing.T) {
	rec := postCompleteTripByOrder(t, `{"order_id":"ORD-NONE","status":"OUT_FOR_DELIVERY"}`,
		service.PaymentUpdateResult{Updated: false, Reason: "no_active_trip"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200, body %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Updated bool   `json:"updated"`
		Reason  string `json:"reason"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Updated || got.Reason != "no_active_trip" {
		t.Fatalf("got %+v, want updated=false reason=no_active_trip", got)
	}
}

func postCompleteTripByOrder(t *testing.T, body string, result service.PaymentUpdateResult, err error) *httptest.ResponseRecorder {
	t.Helper()
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	h := &TripHandlers{
		logger:       logger,
		completeTrip: &stubCompleteTripByOrder{result: result, err: err},
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/trips/complete-by-order", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.CompleteTripByOrder(rec, req)
	return rec
}

func postEditTripByOrder(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	h := &TripHandlers{
		logger:   logger,
		editTrip: &stubEditTripByOrder{result: service.PaymentUpdateResult{Updated: true}},
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/trips/edit-by-order", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.EditTripByOrder(rec, req)
	return rec
}

func assertEditByOrderSucceeded(t *testing.T, rec *httptest.ResponseRecorder, label string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d, want 200, body %s", label, rec.Code, rec.Body.String())
	}
	var got struct {
		Updated bool   `json:"updated"`
		Reason  string `json:"reason"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("%s: decode success body: %v (body %s)", label, err, rec.Body.String())
	}
	if !got.Updated || got.Reason != "" {
		t.Fatalf("%s: got %+v, want updated=true", label, got)
	}
}
