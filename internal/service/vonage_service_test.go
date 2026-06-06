package service

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func generateTestPrivateKey(t *testing.T) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate test RSA key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	pemBytes := pem.EncodeToMemory(block)
	b64 := base64.StdEncoding.EncodeToString(pemBytes)
	return b64, key
}

func TestGenerateVonageJWT_ClaimsAreCorrect(t *testing.T) {
	b64Key, privateKey := generateTestPrivateKey(t)
	appID := "test-app-id-123"

	svc := &VonageService{
		appID:         appID,
		privateKeyB64: b64Key,
	}

	before := time.Now().Unix()
	tokenStr, err := svc.generateJWT()
	after := time.Now().Unix()

	if err != nil {
		t.Fatalf("generateJWT returned error: %v", err)
	}
	if tokenStr == "" {
		t.Fatal("generateJWT returned empty token")
	}

	// Parse and verify with the public key
	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			t.Fatalf("unexpected signing method: %v", token.Header["alg"])
		}
		return &privateKey.PublicKey, nil
	})
	if err != nil {
		t.Fatalf("failed to parse generated JWT: %v", err)
	}
	if !token.Valid {
		t.Fatal("generated JWT is not valid")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("expected MapClaims")
	}

	if claims["application_id"] != appID {
		t.Errorf("application_id: got %v, want %v", claims["application_id"], appID)
	}

	iat := int64(claims["iat"].(float64))
	exp := int64(claims["exp"].(float64))

	if iat < before || iat > after {
		t.Errorf("iat %d not in range [%d, %d]", iat, before, after)
	}
	if exp-iat != 3600 {
		t.Errorf("exp-iat: got %d, want 3600", exp-iat)
	}

	if claims["sub"] != "" {
		t.Errorf("sub: got %v, want empty string", claims["sub"])
	}
	if claims["acl"] != "" {
		t.Errorf("acl: got %v, want empty string", claims["acl"])
	}

	jti, ok := claims["jti"].(string)
	if !ok || jti == "" {
		t.Error("jti must be a non-empty string")
	}
}

func TestGenerateVonageJWT_PartsCount(t *testing.T) {
	b64Key, _ := generateTestPrivateKey(t)
	svc := &VonageService{appID: "x", privateKeyB64: b64Key}

	tokenStr, err := svc.generateJWT()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 3 {
		t.Errorf("JWT must have 3 parts, got %d", len(parts))
	}
}
