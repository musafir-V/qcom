package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/metrics"
	"github.com/sirupsen/logrus"
)

const (
	africaTalkingDefaultBaseURL = "https://api.africastalking.com"
	africaTalkingHTTPTimeout    = 10 * time.Second
	africaTalkingBulkPath       = "/version1/messaging/bulk"
)

type AfricaTalkingSMSService struct {
	username   string
	apiKey     string
	httpClient *http.Client
	logger     *logrus.Logger
	baseURL    string
}

func NewAfricaTalkingSMSService(cfg *config.AfricaTalkingConfig, logger *logrus.Logger) *AfricaTalkingSMSService {
	baseURL := africaTalkingDefaultBaseURL
	if cfg != nil && cfg.BaseURL != "" {
		baseURL = cfg.BaseURL
	}
	username, apiKey := "", ""
	if cfg != nil {
		username = cfg.Username
		apiKey = cfg.APIKey
	}
	return &AfricaTalkingSMSService{
		username:   username,
		apiKey:     apiKey,
		httpClient: metrics.NewInstrumentedClient("africastalking_sms", africaTalkingHTTPTimeout),
		logger:     logger,
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// Configured reports whether Africa's Talking credentials are present.
func (s *AfricaTalkingSMSService) Configured() bool {
	return s != nil && s.username != "" && s.apiKey != ""
}

// SetBaseURL overrides the Africa's Talking API base URL (tests only).
func (s *AfricaTalkingSMSService) SetBaseURL(baseURL string) {
	s.baseURL = strings.TrimRight(baseURL, "/")
}

// SetHTTPClient overrides the HTTP client (tests only).
func (s *AfricaTalkingSMSService) SetHTTPClient(client *http.Client) {
	s.httpClient = client
}

// SendOTP sends an OTP SMS via Africa's Talking bulk messaging API.
func (s *AfricaTalkingSMSService) SendOTP(ctx context.Context, phoneNumber, otp string) error {
	op := logging.Start(ctx, s.logger, "AfricaTalkingSMSService.SendOTP", logrus.Fields{"phone": phoneNumber})
	defer op.End()

	payload := struct {
		Username     string   `json:"username"`
		Message      string   `json:"message"`
		PhoneNumbers []string `json:"phoneNumbers"`
	}{
		Username:     s.username,
		Message:      fmt.Sprintf("Your OTP to log into Bunzo is %s", otp),
		PhoneNumbers: []string{phoneNumber},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to encode africastalking request: %w", err))
	}

	endpoint := s.baseURL + africaTalkingBulkPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return op.Fail(fmt.Errorf("failed to build africastalking request: %w", err))
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apiKey", s.apiKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return op.Fail(fmt.Errorf("africastalking request failed: %w", err))
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := string(raw)
		if len(snippet) > 512 {
			snippet = snippet[:512]
		}
		return op.Fail(fmt.Errorf("africastalking sms returned status %d: %s", resp.StatusCode, snippet))
	}

	op.With("outcome", "sent")
	return nil
}
