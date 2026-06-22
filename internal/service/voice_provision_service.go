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
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// provisionStore is the persistence interface for lazy Vonage user provisioning.
type provisionStore interface {
	IsProvisioned(ctx context.Context, sub string) (bool, error)
	MarkProvisioned(ctx context.Context, sub string) error
}

// VoiceProvisionService lazily and idempotently ensures a Vonage user exists
// for a given sub (cust_<UserID> or de_<DEID>) before voice token issuance.
type VoiceProvisionService struct {
	store         provisionStore
	vonageBaseURL string
	appID         string
	privateKeyB64 string
	httpClient    *http.Client
	logger        *logrus.Logger
}

// NewVoiceProvisionService constructs a VoiceProvisionService.
// privateKeyB64 is the base64-of-PEM RSA private key used to sign app-level JWTs.
func NewVoiceProvisionService(
	store provisionStore,
	vonageBaseURL string,
	appID string,
	privateKeyB64 string,
	logger *logrus.Logger,
) *VoiceProvisionService {
	return &VoiceProvisionService{
		store:         store,
		vonageBaseURL: vonageBaseURL,
		appID:         appID,
		privateKeyB64: privateKeyB64,
		httpClient:    &http.Client{Timeout: 10 * time.Second},
		logger:        logger,
	}
}

// EnsureUser guarantees a Vonage user exists for the given sub.
// If already provisioned (flagged in the store), returns immediately with no
// HTTP call. Otherwise POSTs to the Vonage Users API; a 201 or 409 (user already
// exists on Vonage side) both result in the sub being marked provisioned.
func (s *VoiceProvisionService) EnsureUser(ctx context.Context, sub string) error {
	ok, err := s.store.IsProvisioned(ctx, sub)
	if err != nil {
		return fmt.Errorf("voice provision: check store: %w", err)
	}
	if ok {
		return nil
	}

	token, err := s.signAppLevelJWT()
	if err != nil {
		return fmt.Errorf("voice provision: sign JWT: %w", err)
	}

	body, err := json.Marshal(map[string]string{"name": sub})
	if err != nil {
		return fmt.Errorf("voice provision: marshal body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.vonageBaseURL+"/v0.3/users", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("voice provision: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("voice provision: POST users: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusCreated, resp.StatusCode == http.StatusConflict:
		// 201 Created: user just created. 409 Conflict: user already exists on Vonage.
		// Both are success — mark locally and continue.
		if merr := s.store.MarkProvisioned(ctx, sub); merr != nil {
			return fmt.Errorf("voice provision: mark provisioned: %w", merr)
		}
		return nil
	default:
		return fmt.Errorf("voice provision: Vonage Users API returned %d", resp.StatusCode)
	}
}

// signAppLevelJWT produces an app-level RS256 JWT (empty sub/acl) used to
// authenticate calls to the Vonage REST API on behalf of the application.
func (s *VoiceProvisionService) signAppLevelJWT() (string, error) {
	key, err := s.decodeProvisionKey()
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
	return jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
}

// decodeProvisionKey base64-decodes the stored PEM, then tries PKCS8 then PKCS1.
// This mirrors voice_token_service.go decodeKey.
func (s *VoiceProvisionService) decodeProvisionKey() (*rsa.PrivateKey, error) {
	der, err := base64.StdEncoding.DecodeString(s.privateKeyB64)
	if err != nil {
		return nil, fmt.Errorf("provision key not base64: %w", err)
	}
	block, _ := pem.Decode(der)
	if block == nil {
		return nil, fmt.Errorf("provision key not PEM")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rk, ok := k.(*rsa.PrivateKey); ok {
			return rk, nil
		}
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}
