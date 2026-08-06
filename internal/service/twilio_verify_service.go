package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/metrics"
	"github.com/sirupsen/logrus"
)

const (
	twilioVerifyBaseURL = "https://verify.twilio.com/v2/Services"
	twilioHTTPTimeout   = 10 * time.Second
)

type TwilioVerifyService struct {
	accountSID string
	authToken  string
	serviceSID string
	httpClient *http.Client
	logger     *logrus.Logger
	baseURL    string
}

func NewTwilioVerifyService(cfg *config.TwilioConfig, logger *logrus.Logger) *TwilioVerifyService {
	return &TwilioVerifyService{
		accountSID: cfg.AccountSID,
		authToken:  cfg.AuthToken,
		serviceSID: cfg.VerifyServiceSID,
		httpClient: metrics.NewInstrumentedClient("twilio_verify", twilioHTTPTimeout),
		logger:     logger,
		baseURL:    twilioVerifyBaseURL,
	}
}

// SetBaseURL overrides the Twilio Verify base URL (tests only).
func (s *TwilioVerifyService) SetBaseURL(baseURL string) {
	s.baseURL = strings.TrimRight(baseURL, "/")
}

// SetHTTPClient overrides the HTTP client (tests only).
func (s *TwilioVerifyService) SetHTTPClient(client *http.Client) {
	s.httpClient = client
}

// StartSMSVerification asks Twilio to send an SMS OTP to phoneNumber (E.164).
func (s *TwilioVerifyService) StartSMSVerification(ctx context.Context, phoneNumber string) error {
	op := logging.Start(ctx, s.logger, "TwilioVerifyService.StartSMSVerification", logrus.Fields{"phone": phoneNumber})
	defer op.End()

	form := url.Values{}
	form.Set("To", phoneNumber)
	form.Set("Channel", "sms")

	status, body, err := s.postForm(ctx, s.serviceSID+"/Verifications", form)
	if err != nil {
		return op.Fail(err)
	}
	if status < 200 || status >= 300 {
		return op.Fail(fmt.Errorf("twilio verify start returned status %d: %s", status, body))
	}

	op.With("outcome", "started")
	return nil
}

// CheckVerification asks Twilio whether code is valid for phoneNumber.
func (s *TwilioVerifyService) CheckVerification(ctx context.Context, phoneNumber, code string) (bool, error) {
	op := logging.Start(ctx, s.logger, "TwilioVerifyService.CheckVerification", logrus.Fields{"phone": phoneNumber})
	defer op.End()

	form := url.Values{}
	form.Set("To", phoneNumber)
	form.Set("Code", code)

	status, body, err := s.postForm(ctx, s.serviceSID+"/VerificationCheck", form)
	if err != nil {
		return false, op.Fail(err)
	}

	// Twilio returns 404 when there is no pending verification for the number.
	if status == http.StatusNotFound {
		op.With("outcome", "not_found")
		return false, nil
	}
	if status < 200 || status >= 300 {
		return false, op.Fail(fmt.Errorf("twilio verify check returned status %d: %s", status, body))
	}

	var resp struct {
		Status string `json:"status"`
		Valid  bool   `json:"valid"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return false, op.Fail(fmt.Errorf("failed to decode twilio verify check response: %w", err))
	}

	approved := resp.Valid || strings.EqualFold(resp.Status, "approved")
	if approved {
		op.With("outcome", "approved")
		return true, nil
	}
	op.With("outcome", "rejected")
	return false, nil
}

func (s *TwilioVerifyService) postForm(ctx context.Context, pathSuffix string, form url.Values) (int, string, error) {
	endpoint := s.baseURL + "/" + strings.TrimPrefix(pathSuffix, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, "", fmt.Errorf("failed to build twilio request: %w", err)
	}
	req.SetBasicAuth(s.accountSID, s.authToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("twilio request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, string(raw), nil
}
