package service

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/qcom/qcom/internal/config"
	"github.com/sirupsen/logrus"
)

// testRSAKeyB64 generates a fresh 2048-bit RSA key, marshals it as PKCS1,
// PEM-encodes it, base64-encodes the PEM bytes, and returns the string.
// This mirrors the base64-of-PEM format stored in config.VoiceConfig.PrivateKeyB64.
func testRSAKeyB64(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("testRSAKeyB64: generate key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	pemBytes := pem.EncodeToMemory(block)
	return base64.StdEncoding.EncodeToString(pemBytes)
}

func TestGenerateUserTokenClaims(t *testing.T) {
	// base64 of a PKCS1 test key generated for the test; helper below.
	cfg := config.VoiceConfig{AppID: "app-1", PrivateKeyB64: testRSAKeyB64(t)}
	s := NewVoiceTokenService(cfg, logrus.New())

	tok, err := s.GenerateUserToken("cust_U9")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	claims := jwt.MapClaims{}
	_, _, err = jwt.NewParser().ParseUnverified(tok, claims)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims["application_id"] != "app-1" {
		t.Errorf("app_id = %v", claims["application_id"])
	}
	if claims["sub"] != "cust_U9" {
		t.Errorf("sub = %v", claims["sub"])
	}
	if _, ok := claims["acl"]; !ok {
		t.Errorf("acl missing")
	}
	exp := int64(claims["exp"].(float64))
	iat := int64(claims["iat"].(float64))
	if exp-iat != 3600 {
		t.Errorf("ttl = %d, want 3600", exp-iat)
	}
}
