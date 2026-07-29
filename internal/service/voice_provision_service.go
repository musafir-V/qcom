package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/qcom/qcom/internal/metrics"
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
		httpClient:    metrics.NewInstrumentedClient("vonage_voice", 10*time.Second),
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
	return signVonageJWT(s.privateKeyB64, "provision key", s.appID, nil)
}

