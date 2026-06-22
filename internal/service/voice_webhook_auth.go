package service

import (
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// VerifyVonageSignature verifies that authHeader is a valid "Bearer <jwt>"
// where the JWT is HS256-signed with secret. Returns false for any other
// input including empty headers, missing Bearer prefix, wrong secret, or
// non-HMAC signing methods (alg confusion prevention).
func VerifyVonageSignature(authHeader, secret string) bool {
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return false
	}
	raw := strings.TrimPrefix(authHeader, "Bearer ")
	if raw == "" {
		return false
	}
	_, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(secret), nil
	})
	return err == nil
}
