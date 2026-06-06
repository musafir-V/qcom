package service

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

const vonageMessagesURL = "https://api.nexmo.com/v1/messages"
const vonageTemplateName = "bunzo_login_otp"

type VonageService struct {
	appID         string
	privateKeyB64 string
	whatsAppFrom  string
	jwtRepo       *repository.VonageJWTRepository
	httpClient    *http.Client
	logger        *logrus.Logger
}

func NewVonageService(cfg *config.VonageConfig, jwtRepo *repository.VonageJWTRepository, logger *logrus.Logger) *VonageService {
	return &VonageService{
		appID:         cfg.AppID,
		privateKeyB64: cfg.PrivateKeyB64,
		whatsAppFrom:  cfg.WhatsAppFrom,
		jwtRepo:       jwtRepo,
		httpClient:    &http.Client{Timeout: 10 * time.Second},
		logger:        logger,
	}
}

// getOrRefreshJWT returns a valid JWT from DynamoDB cache, generating a new one if needed.
func (s *VonageService) getOrRefreshJWT(ctx context.Context) (string, error) {
	cached, err := s.jwtRepo.Get(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to read JWT cache: %w", err)
	}
	if cached != "" {
		return cached, nil
	}

	token, err := s.generateJWT()
	if err != nil {
		return "", fmt.Errorf("failed to generate Vonage JWT: %w", err)
	}

	if err := s.jwtRepo.Store(ctx, token); err != nil {
		// Non-fatal: log and continue; we have a valid token for this request.
		s.logger.WithError(err).Warn("Failed to cache Vonage JWT in DynamoDB")
	}

	return token, nil
}

// generateJWT creates a signed RS256 JWT for the Vonage Messages API.
// Uses jwt.MapClaims to guarantee exact claim serialization with no field shadowing.
func (s *VonageService) generateJWT() (string, error) {
	privateKey, err := s.decodePrivateKey()
	if err != nil {
		return "", err
	}

	now := time.Now().Unix()

	claims := jwt.MapClaims{
		"application_id": s.appID,
		"sub":            "",
		"acl":            "",
		"iat":            now,
		"exp":            now + 3600,
		"jti":            uuid.New().String(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(privateKey)
}

func (s *VonageService) decodePrivateKey() (*rsa.PrivateKey, error) {
	pemBytes, err := base64.StdEncoding.DecodeString(s.privateKeyB64)
	if err != nil {
		return nil, fmt.Errorf("failed to base64-decode Vonage private key: %w", err)
	}

	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block from Vonage private key")
	}

	// Try PKCS8 first (Vonage typically provides PKCS8), fall back to PKCS1.
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("Vonage private key is not an RSA key")
		}
		return rsaKey, nil
	}

	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

// SendWhatsAppOTP delivers a 6-digit OTP via the bunzo_login_otp WhatsApp template.
func (s *VonageService) SendWhatsAppOTP(ctx context.Context, phoneNumber, otp string) error {
	op := logging.Start(ctx, s.logger, "VonageService.SendWhatsAppOTP", logrus.Fields{"phone": phoneNumber})
	defer op.End()

	jwtToken, err := s.getOrRefreshJWT(ctx)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to get Vonage JWT: %w", err))
	}

	// Vonage expects the phone number without leading '+'.
	to := strings.TrimPrefix(phoneNumber, "+")

	body := map[string]interface{}{
		"to":           to,
		"from":         s.whatsAppFrom,
		"channel":      "whatsapp",
		"message_type": "custom",
		"custom": map[string]interface{}{
			"type": "template",
			"template": map[string]interface{}{
				"name": vonageTemplateName,
				"language": map[string]string{
					"policy": "deterministic",
					"code":   "en_US",
				},
				"components": []map[string]interface{}{
					{
						"type": "body",
						"parameters": []map[string]string{
							{"type": "text", "text": otp},
						},
					},
					{
						"type":     "button",
						"sub_type": "url",
						"index":    0,
						"parameters": []map[string]string{
							{"type": "text", "text": otp},
						},
					},
				},
			},
		},
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal Vonage request: %w", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, vonageMessagesURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return op.Fail(fmt.Errorf("failed to build Vonage request: %w", err))
	}
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return op.Fail(fmt.Errorf("Vonage API request failed: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errBody)
		return op.Fail(fmt.Errorf("Vonage API returned status %d: %v", resp.StatusCode, errBody))
	}

	op.With("outcome", "sent")
	return nil
}
