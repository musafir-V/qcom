package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

// The payout-config repository is a concrete DynamoDB-backed type, so these
// tests cover the request-validation branches, which reject before any write.
func newConfigHandlers() *ConfigHandlers {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return NewConfigHandlers(nil, logger)
}

func TestUpdatePayoutConfig_RejectsInvalidRequests(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantCode string
	}{
		{"malformed body", `{`, "INVALID_REQUEST"},
		{"missing field name", `{"value":"25.5"}`, "MISSING_FIELD"},
		{"missing value", `{"field":"referral_bonus_zmw"}`, "MISSING_FIELD"},
		{"both empty", `{"field":"","value":""}`, "MISSING_FIELD"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()

			newConfigHandlers().UpdatePayoutConfig(rec,
				httptest.NewRequest(http.MethodPatch, "/api/v1/config/payout", strings.NewReader(tc.body)))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			if got := decodeErrorCode(t, rec); got != tc.wantCode {
				t.Fatalf("error code = %q, want %q", got, tc.wantCode)
			}
		})
	}
}

func TestUpdatePayoutConfig_ValidRequestReachesRepository(t *testing.T) {
	for _, body := range []string{
		`{"field":"referral_bonus_zmw","value":"25.5"}`,
		`{"field":"payout_mode","value":"weekly"}`,
	} {
		t.Run(body, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected validation to pass and the repository write to be attempted")
				}
			}()

			newConfigHandlers().UpdatePayoutConfig(httptest.NewRecorder(),
				httptest.NewRequest(http.MethodPatch, "/api/v1/config/payout", strings.NewReader(body)))
		})
	}
}
