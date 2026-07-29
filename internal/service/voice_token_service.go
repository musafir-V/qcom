package service

import (
	"github.com/golang-jwt/jwt/v5"
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
	return signVonageJWT(s.privateKeyB64, "voice key", s.appID, jwt.MapClaims{
		"sub": sub,
		"acl": voiceACL(),
	})
}

