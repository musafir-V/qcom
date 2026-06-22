package service

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestVerifyVonageSignature(t *testing.T) {
	secret := "sek"
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"iat": time.Now().Unix()})
	signed, _ := tok.SignedString([]byte(secret))
	if !VerifyVonageSignature("Bearer "+signed, secret) {
		t.Fatal("valid signature rejected")
	}
	if VerifyVonageSignature("Bearer "+signed, "wrong") {
		t.Fatal("invalid secret accepted")
	}
	if VerifyVonageSignature("", secret) {
		t.Fatal("empty header accepted")
	}
}
