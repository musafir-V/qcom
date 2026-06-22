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
	// Test algorithm confusion: alg:none token must be rejected
	noneTok, _ := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"iat": time.Now().Unix()}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if VerifyVonageSignature("Bearer "+noneTok, secret) {
		t.Fatal("alg:none token must be rejected")
	}
	// Test non-Bearer prefix: token without Bearer prefix must be rejected
	if VerifyVonageSignature("Token "+signed, secret) {
		t.Fatal("non-Bearer header must be rejected")
	}
}
