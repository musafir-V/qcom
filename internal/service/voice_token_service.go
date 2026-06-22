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
	"github.com/qcom/qcom/internal/config"
	"github.com/sirupsen/logrus"
)

type VoiceTokenService struct {
	appID         string
	privateKeyB64 string
	logger        *logrus.Logger
}

func NewVoiceTokenService(cfg config.VoiceConfig, logger *logrus.Logger) *VoiceTokenService {
	return &VoiceTokenService{appID: cfg.AppID, privateKeyB64: cfg.PrivateKeyB64, logger: logger}
}

// voiceACL returns the standard Vonage Client SDK ACL for in-app voice.
func voiceACL() map[string]any {
	empty := map[string]any{}
	return map[string]any{"paths": map[string]any{
		"/*/users/**":         empty,
		"/*/conversations/**": empty,
		"/*/sessions/**":      empty,
		"/*/devices/**":       empty,
		"/*/push/**":          empty,
		"/*/knocking/**":      empty,
		"/*/legs/**":          empty,
	}}
}

// GenerateUserToken signs a per-user RS256 JWT for the Vonage Client SDK login.
// The token is valid for 3600 seconds and carries the standard in-app voice ACL.
func (s *VoiceTokenService) GenerateUserToken(sub string) (string, error) {
	key, err := s.decodeKey()
	if err != nil {
		return "", err
	}
	now := time.Now().Unix()
	claims := jwt.MapClaims{
		"application_id": s.appID,
		"sub":            sub,
		"acl":            voiceACL(),
		"iat":            now,
		"exp":            now + 3600,
		"jti":            uuid.New().String(),
	}
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
}

// decodeKey base64-decodes the stored PEM, then tries PKCS8 then PKCS1.
// This mirrors the approach in vonage_service.go decodePrivateKey.
func (s *VoiceTokenService) decodeKey() (*rsa.PrivateKey, error) {
	der, err := base64.StdEncoding.DecodeString(s.privateKeyB64)
	if err != nil {
		return nil, fmt.Errorf("voice key not base64: %w", err)
	}
	block, _ := pem.Decode(der)
	if block == nil {
		return nil, fmt.Errorf("voice key not PEM")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rk, ok := k.(*rsa.PrivateKey); ok {
			return rk, nil
		}
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}
