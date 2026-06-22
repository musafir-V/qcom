package handlers

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qcom/qcom/internal/config"
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

func newTestVoiceHandlers(t *testing.T) *VoiceHandlers {
	t.Helper()
	cfg := config.VoiceConfig{AppID: "test-app", PrivateKeyB64: testVoiceRSAKeyB64(t)}
	tokenSvc := service.NewVoiceTokenService(cfg, logrus.New())
	return NewVoiceHandlers(tokenSvc, &fakeEnsurer{}, logrus.New())
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
