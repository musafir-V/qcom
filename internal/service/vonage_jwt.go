// internal/service/vonage_jwt.go
package service

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// vonageJWTTTL is how long every Vonage JWT (app-level and per-user) is valid.
const vonageJWTTTL = 3600

// decodeRSAPrivateKeyB64 decodes a base64-of-PEM RSA private key, accepting
// either PKCS8 (what Vonage hands out) or PKCS1. label names the key in errors.
func decodeRSAPrivateKeyB64(privateKeyB64, label string) (*rsa.PrivateKey, error) {
	der, err := base64.StdEncoding.DecodeString(privateKeyB64)
	if err != nil {
		return nil, fmt.Errorf("%s not base64: %w", label, err)
	}
	block, _ := pem.Decode(der)
	if block == nil {
		return nil, fmt.Errorf("%s not PEM", label)
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%s is not an RSA key", label)
		}
		return rsaKey, nil
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

// signVonageJWT signs an RS256 Vonage JWT valid for vonageJWTTTL seconds. It
// carries application_id/iat/exp/jti plus any extra claims (sub, acl) the
// caller needs. jwt.MapClaims is used throughout to guarantee exact claim
// serialization with no field shadowing.
func signVonageJWT(privateKeyB64, label, appID string, extra jwt.MapClaims) (string, error) {
	key, err := decodeRSAPrivateKeyB64(privateKeyB64, label)
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	claims := jwt.MapClaims{
		"application_id": appID,
		"iat":            now,
		"exp":            now + vonageJWTTTL,
		"jti":            uuid.New().String(),
	}
	for k, v := range extra {
		claims[k] = v
	}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
}
