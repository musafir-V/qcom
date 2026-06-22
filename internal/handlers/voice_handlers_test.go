package handlers

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

// testVoiceRSAKeyB64 generates a 2048-bit RSA key, PEM-encodes it (PKCS1),
// base64-encodes the PEM bytes and returns the string — same format as
// config.VoiceConfig.PrivateKeyB64.
func testVoiceRSAKeyB64(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("testVoiceRSAKeyB64: generate key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	pemBytes := pem.EncodeToMemory(block)
	return base64.StdEncoding.EncodeToString(pemBytes)
}

type fakeEnsurer struct{}

func (f *fakeEnsurer) EnsureUser(_ context.Context, _ string) error { return nil }

// fakeTripGetter returns the configured trip or an error if nil.
type fakeTripGetter struct{ trip *models.Trip }

func (f *fakeTripGetter) GetByID(_ context.Context, _ string) (*models.Trip, error) {
	if f.trip == nil {
		return nil, errors.New("not found")
	}
	return f.trip, nil
}

// fakeCallCounter returns the configured count.
type fakeCallCounter struct{ count int }

func (f *fakeCallCounter) CountByTripDirection(_ context.Context, _, _ string) (int, error) {
	return f.count, nil
}

// testVoiceOption is a functional option for newTestVoiceHandlers.
type testVoiceOption func(*testVoiceOptions)

type testVoiceOptions struct {
	trip  *models.Trip
	count int
}

func withTrip(trip *models.Trip) testVoiceOption {
	return func(o *testVoiceOptions) { o.trip = trip }
}

func withCallCount(n int) testVoiceOption {
	return func(o *testVoiceOptions) { o.count = n }
}

func newTestVoiceHandlers(t *testing.T, opts ...testVoiceOption) *VoiceHandlers {
	t.Helper()
	o := &testVoiceOptions{}
	for _, opt := range opts {
		opt(o)
	}
	cfg := config.VoiceConfig{AppID: "test-app", PrivateKeyB64: testVoiceRSAKeyB64(t)}
	tokenSvc := service.NewVoiceTokenService(cfg, logrus.New())
	return NewVoiceHandlers(
		tokenSvc,
		&fakeEnsurer{},
		&fakeTripGetter{trip: o.trip},
		&fakeCallCounter{count: o.count},
		logrus.New(),
	)
}

func TestPostTokenReturnsTokenForDE(t *testing.T) {
	h := newTestVoiceHandlers(t) // wires real VoiceTokenService (test key) + fake provisioner
	req := httptest.NewRequest("POST", "/api/v1/voice/token", nil)
	ctx := context.WithValue(req.Context(), "entity_id", "D1")
	ctx = context.WithValue(ctx, "entity_type", "de")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.PostToken(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var body struct{ Token, User string }
	json.Unmarshal(rr.Body.Bytes(), &body)
	if body.User != "de_D1" || body.Token == "" {
		t.Fatalf("body = %+v", body)
	}
}

func TestAnswerWebhookConnectsWhenEligible(t *testing.T) {
	trip := &models.Trip{TripID: "T1", DEID: "D1", CustomerUserID: "U9",
		Status: models.TripStatusOutForDelivery}
	h := newTestVoiceHandlers(t, withTrip(trip), withCallCount(0))

	body := `{"from":"de_D1","custom_data":{"trip_id":"T1","direction":"de_to_cust"}}`
	req := httptest.NewRequest("POST", "/webhooks/voice/answer", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.AnswerWebhook(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var ncco []map[string]any
	json.Unmarshal(rr.Body.Bytes(), &ncco)
	if ncco[0]["action"] != "connect" {
		t.Fatalf("expected connect, got %+v", ncco[0])
	}
}

func TestAnswerWebhookRejectsWhenNotCallable(t *testing.T) {
	trip := &models.Trip{TripID: "T1", DEID: "D1", CustomerUserID: "U9",
		Status: models.TripStatusAccepted}
	h := newTestVoiceHandlers(t, withTrip(trip), withCallCount(0))
	body := `{"from":"de_D1","custom_data":{"trip_id":"T1","direction":"de_to_cust"}}`
	req := httptest.NewRequest("POST", "/webhooks/voice/answer", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.AnswerWebhook(rr, req)
	var ncco []map[string]any
	json.Unmarshal(rr.Body.Bytes(), &ncco)
	if ncco[0]["action"] != "talk" {
		t.Fatalf("expected reject talk, got %+v", ncco[0])
	}
}

func TestAnswerWebhookRejectsOverCap(t *testing.T) {
	trip := &models.Trip{TripID: "T1", DEID: "D1", CustomerUserID: "U9",
		Status: models.TripStatusOutForDelivery}
	h := newTestVoiceHandlers(t, withTrip(trip), withCallCount(models.CallCapPerDirection))
	body := `{"from":"de_D1","custom_data":{"trip_id":"T1","direction":"de_to_cust"}}`
	req := httptest.NewRequest("POST", "/webhooks/voice/answer", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.AnswerWebhook(rr, req)
	var ncco []map[string]any
	json.Unmarshal(rr.Body.Bytes(), &ncco)
	if ncco[0]["action"] != "talk" {
		t.Fatalf("expected reject over cap, got %+v", ncco[0])
	}
}
