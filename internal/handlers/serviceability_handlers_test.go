package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qcom/qcom/internal/middleware"
	"github.com/sirupsen/logrus"
)

// The serviceability service is a concrete DynamoDB-backed type, so these
// tests cover the request-validation branches, all of which reject before the
// service is ever called.
func newServiceabilityHandlers() *ServiceabilityHandlers {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return NewServiceabilityHandlers(nil, logger)
}

func TestCheckServiceability_RejectsInvalidRequests(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		entityID   string
		entityType string
		wantStatus int
		wantCode   string
	}{
		{"anonymous caller", `{"latitude":-15.4,"longitude":28.3}`, "", "customer", http.StatusUnauthorized, "UNAUTHORIZED"},
		{"malformed body", `{`, "cust-1", "customer", http.StatusBadRequest, "INVALID_REQUEST"},
		{"missing latitude", `{"longitude":28.3}`, "cust-1", "customer", http.StatusBadRequest, "MISSING_FIELD"},
		{"missing longitude", `{"latitude":-15.4}`, "cust-1", "customer", http.StatusBadRequest, "MISSING_FIELD"},
		{"latitude out of range", `{"latitude":91,"longitude":28.3}`, "cust-1", "customer", http.StatusBadRequest, "INVALID_COORDINATES"},
		{"longitude out of range", `{"latitude":-15.4,"longitude":-181}`, "cust-1", "customer", http.StatusBadRequest, "INVALID_COORDINATES"},
		{"guest with malformed body", `{`, "", middleware.EntityTypeGuest, http.StatusBadRequest, "INVALID_REQUEST"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()

			newServiceabilityHandlers().CheckServiceability(rec,
				authedRequest(http.MethodPost, "/api/v1/serviceability", tc.body, tc.entityID, tc.entityType))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := decodeErrorCode(t, rec); got != tc.wantCode {
				t.Fatalf("error code = %q, want %q", got, tc.wantCode)
			}
		})
	}
}

// A zero coordinate is a legitimate location, so it must not be treated as a
// missing field — the handler is expected to get past validation and reach the
// service (which is nil here, hence the panic).
func TestCheckServiceability_ZeroCoordinatesPassValidation(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected validation to pass and the service call to be attempted")
		}
	}()

	newServiceabilityHandlers().CheckServiceability(httptest.NewRecorder(),
		authedRequest(http.MethodPost, "/api/v1/serviceability", `{"latitude":0,"longitude":0}`, "cust-1", "customer"))
}
