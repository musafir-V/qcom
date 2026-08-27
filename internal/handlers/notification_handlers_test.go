package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type fakeNotificationService struct {
	sendResult models.NotificationSendResult
	sentReq    models.NotificationSendRequest

	upsertErr       error
	upsertRecipient models.RecipientType
	upsertID        string
	upsertToken     string
	upsertPlatform  string
}

func (f *fakeNotificationService) Send(_ context.Context, req models.NotificationSendRequest) models.NotificationSendResult {
	f.sentReq = req
	return f.sendResult
}

func (f *fakeNotificationService) UpsertDeviceToken(_ context.Context, recipientType models.RecipientType, recipientID, token, platform string) error {
	f.upsertRecipient = recipientType
	f.upsertID = recipientID
	f.upsertToken = token
	f.upsertPlatform = platform
	return f.upsertErr
}

func (f *fakeNotificationService) ClearDeviceToken(context.Context, models.RecipientType, string) error {
	return nil
}

func newNotificationHandlers(svc *fakeNotificationService) *NotificationHandlers {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return NewNotificationHandlers(svc, logger)
}

func authedRequest(method, target, body, entityID, entityType string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	ctx := context.WithValue(req.Context(), "entity_id", entityID)
	ctx = context.WithValue(ctx, "entity_type", entityType)
	return req.WithContext(ctx)
}

func decodeErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not an error response: %v", rec.Body.String(), err)
	}
	return body.Error.Code
}

func TestPutDeviceToken_Success(t *testing.T) {
	svc := &fakeNotificationService{}
	rec := httptest.NewRecorder()

	newNotificationHandlers(svc).PutDeviceToken(rec,
		authedRequest(http.MethodPut, "/api/v1/device-token", `{"fcm_token":"  tok-1  ","platform":" android "}`, "de-1", "de"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if svc.upsertRecipient != models.RecipientTypeDriver || svc.upsertID != "de-1" {
		t.Fatalf("recipient = (%q, %q), want (driver, de-1)", svc.upsertRecipient, svc.upsertID)
	}
	if svc.upsertToken != "tok-1" || svc.upsertPlatform != "android" {
		t.Fatalf("token/platform = (%q, %q), want them trimmed", svc.upsertToken, svc.upsertPlatform)
	}
}

func TestPutDeviceToken_MapsCustomerToken(t *testing.T) {
	svc := &fakeNotificationService{}
	rec := httptest.NewRecorder()

	newNotificationHandlers(svc).PutDeviceToken(rec,
		authedRequest(http.MethodPut, "/api/v1/device-token", `{"fcm_token":"tok"}`, "cust-1", "customer"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.upsertRecipient != models.RecipientTypeCustomer {
		t.Fatalf("recipient type = %q, want customer", svc.upsertRecipient)
	}
}

func TestPutDeviceToken_Errors(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		entityID   string
		entityType string
		upsertErr  error
		wantStatus int
		wantCode   string
	}{
		{"missing identity", `{"fcm_token":"tok"}`, "", "de", nil, http.StatusUnauthorized, "UNAUTHORIZED"},
		{"unsupported entity type", `{"fcm_token":"tok"}`, "adm-1", "admin", nil, http.StatusForbidden, "FORBIDDEN"},
		{"malformed body", `{`, "de-1", "de", nil, http.StatusBadRequest, "INVALID_REQUEST"},
		{"service failure", `{"fcm_token":"tok"}`, "de-1", "de", errors.New("boom"), http.StatusInternalServerError, "DEVICE_TOKEN_UPDATE_FAILED"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeNotificationService{upsertErr: tc.upsertErr}
			rec := httptest.NewRecorder()

			newNotificationHandlers(svc).PutDeviceToken(rec,
				authedRequest(http.MethodPut, "/api/v1/device-token", tc.body, tc.entityID, tc.entityType))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := decodeErrorCode(t, rec); got != tc.wantCode {
				t.Fatalf("error code = %q, want %q", got, tc.wantCode)
			}
		})
	}
}

func TestSendNotification_Accepted(t *testing.T) {
	svc := &fakeNotificationService{sendResult: models.NotificationSendResult{
		Status:    models.SendStatusSent,
		MessageID: "msg-1",
	}}
	rec := httptest.NewRecorder()
	body := `{"recipient_type":"driver","recipient_id":"de-1","event_type":"trip_assigned","title":"T","body":"B"}`

	newNotificationHandlers(svc).SendNotification(rec, httptest.NewRequest(http.MethodPost, "/internal/v1/notifications/send", strings.NewReader(body)))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if svc.sentReq.RecipientID != "de-1" || svc.sentReq.EventType != "trip_assigned" {
		t.Fatalf("forwarded request = %+v", svc.sentReq)
	}

	var result models.NotificationSendResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("body %q is not a send result: %v", rec.Body.String(), err)
	}
	if result.Status != models.SendStatusSent || result.MessageID != "msg-1" {
		t.Fatalf("result = %+v, want the service's result echoed back", result)
	}
}

func TestSendNotification_SkippedIsStillAccepted(t *testing.T) {
	svc := &fakeNotificationService{sendResult: models.NotificationSendResult{
		Status: models.SendStatusSkipped,
		Reason: "no device token",
	}}
	rec := httptest.NewRecorder()

	newNotificationHandlers(svc).SendNotification(rec,
		httptest.NewRequest(http.MethodPost, "/internal/v1/notifications/send", strings.NewReader(`{"recipient_id":"de-1"}`)))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 even when the send is skipped", rec.Code)
	}
}

func TestSendNotification_MalformedBody(t *testing.T) {
	rec := httptest.NewRecorder()

	newNotificationHandlers(&fakeNotificationService{}).SendNotification(rec,
		httptest.NewRequest(http.MethodPost, "/internal/v1/notifications/send", strings.NewReader(`{`)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := decodeErrorCode(t, rec); got != "INVALID_REQUEST" {
		t.Fatalf("error code = %q, want INVALID_REQUEST", got)
	}
}
