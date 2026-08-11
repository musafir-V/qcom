package service

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

const masterOTPBypass = "112233"

type twilioOTPVerifier interface {
	StartSMSVerification(ctx context.Context, phoneNumber string) error
	CheckVerification(ctx context.Context, phoneNumber, code string) (bool, error)
}

type africaTalkingOTPSender interface {
	Configured() bool
	SendOTP(ctx context.Context, phoneNumber, otp string) error
}

type otpStore interface {
	Store(ctx context.Context, phoneNumber string, otpData models.OTPData) error
	Get(ctx context.Context, phoneNumber string) (*models.OTPData, error)
	Delete(ctx context.Context, phoneNumber string) error
}

type smsOTPRoutingConfig interface {
	Get(ctx context.Context) (*models.SMSOTPRoutingConfig, error)
}

type OTPService struct {
	twilio  twilioOTPVerifier
	at      africaTalkingOTPSender
	otpRepo otpStore
	routing smsOTPRoutingConfig
	cfg     *config.OTPConfig
	logger  *logrus.Logger
}

func NewOTPService(
	twilio *TwilioVerifyService,
	at africaTalkingOTPSender,
	otpRepo otpStore,
	routing smsOTPRoutingConfig,
	otpCfg *config.OTPConfig,
	logger *logrus.Logger,
) *OTPService {
	if otpCfg == nil {
		otpCfg = &config.OTPConfig{
			Length:      6,
			Expiry:      10 * time.Minute,
			MaxAttempts: 5,
		}
	}
	return &OTPService{
		twilio:  twilio,
		at:      at,
		otpRepo: otpRepo,
		routing: routing,
		cfg:     otpCfg,
		logger:  logger,
	}
}

// GenerateOTP routes to Africa's Talking (default) or Twilio Verify.
// MTN Zambia prefixes +26076 / +26096 always use Twilio when the kill-switch
// is off. The OTP string is never returned to callers (Twilio owns its codes;
// AT codes stay in the local store). On provider failure after failover,
// returns soft success ("", nil) so clients can still use masterOTPBypass.
func (s *OTPService) GenerateOTP(ctx context.Context, phoneNumber string) (string, error) {
	op := logging.Start(ctx, s.logger, "GenerateOTP", logrus.Fields{"phone": phoneNumber})
	defer op.End()

	forceTwilio := s.forceTwilio(ctx)
	op.With("force_twilio", forceTwilio)

	if shouldSendViaAfricaTalking(phoneNumber, forceTwilio, s.atConfigured()) {
		return s.generateViaAfricaTalking(ctx, op, phoneNumber)
	}
	return s.generateViaTwilio(ctx, op, phoneNumber)
}

func (s *OTPService) generateViaAfricaTalking(ctx context.Context, op *logging.Op, phoneNumber string) (string, error) {
	op.With("provider", "africastalking")

	if existing, err := s.getLocalOTP(ctx, phoneNumber); err == nil && isOTPReusable(existing, time.Now(), s.cfg.MaxAttempts) {
		if err := s.at.SendOTP(ctx, phoneNumber, existing.OTP); err != nil {
			op.Logger().WithError(err).Warn("Failed to resend OTP via Africa's Talking; failing over to Twilio")
			s.deleteLocalOTP(ctx, phoneNumber)
			return s.generateViaTwilio(ctx, op, phoneNumber)
		}
		op.With("outcome", "resent")
		return "", nil
	}

	otp, err := generateRandomOTP(s.cfg.Length)
	if err != nil {
		return "", op.Fail(fmt.Errorf("failed to generate OTP: %w", err))
	}

	if err := s.at.SendOTP(ctx, phoneNumber, otp); err != nil {
		op.Logger().WithError(err).Warn("Failed to send OTP via Africa's Talking; failing over to Twilio")
		s.deleteLocalOTP(ctx, phoneNumber)
		return s.generateViaTwilio(ctx, op, phoneNumber)
	}

	hashedOTP, err := bcrypt.GenerateFromPassword([]byte(otp), bcrypt.DefaultCost)
	if err != nil {
		return "", op.Fail(fmt.Errorf("failed to hash OTP: %w", err))
	}

	otpData := models.OTPData{
		OTP:       otp,
		OTPHash:   string(hashedOTP),
		Phone:     phoneNumber,
		Attempts:  0,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(s.cfg.Expiry),
	}
	if err := s.otpRepo.Store(ctx, phoneNumber, otpData); err != nil {
		return "", op.Fail(err)
	}

	op.With("outcome", "sent")
	return "", nil
}

func (s *OTPService) generateViaTwilio(ctx context.Context, op *logging.Op, phoneNumber string) (string, error) {
	op.With("provider", "twilio")

	// Best-effort: clear any stale AT OTP so it cannot shadow Twilio verify.
	s.deleteLocalOTP(ctx, phoneNumber)

	if err := s.twilio.StartSMSVerification(ctx, phoneNumber); err != nil {
		op.Logger().WithError(err).Warn("Failed to start Twilio Verify SMS; returning success for bypass")
		op.With("outcome", "twilio_failed")
		return "", nil
	}

	op.With("outcome", "sent")
	return "", nil
}

func (s *OTPService) VerifyOTP(ctx context.Context, phoneNumber, otp string) (bool, error) {
	op := logging.Start(ctx, s.logger, "VerifyOTP", logrus.Fields{"phone": phoneNumber})
	defer op.End()

	if otp == masterOTPBypass {
		op.With("outcome", "master_bypass")
		return true, nil
	}

	forceTwilio := s.forceTwilio(ctx)
	op.With("force_twilio", forceTwilio)

	if forceTwilio || isMTNZambiaTwilioPrefix(phoneNumber) {
		return s.verifyViaTwilio(ctx, op, phoneNumber, otp)
	}

	// Local OTP wins when present (AT path / AT success).
	// Otherwise Twilio CheckVerification (failover / in-flight Twilio OTP).
	otpData, err := s.getLocalOTP(ctx, phoneNumber)
	if err == nil && otpData != nil {
		return s.verifyLocalOTP(ctx, op, phoneNumber, otp, otpData)
	}

	return s.verifyViaTwilio(ctx, op, phoneNumber, otp)
}

func (s *OTPService) verifyLocalOTP(ctx context.Context, op *logging.Op, phoneNumber, otp string, otpData *models.OTPData) (bool, error) {
	op.With("provider", "local")

	if time.Now().After(otpData.ExpiresAt) {
		s.deleteLocalOTP(ctx, phoneNumber)
		return false, op.Outcome("expired", fmt.Errorf("OTP expired"))
	}

	if otpData.Attempts >= s.cfg.MaxAttempts {
		s.deleteLocalOTP(ctx, phoneNumber)
		return false, op.Outcome("max_attempts", fmt.Errorf("maximum attempts exceeded"))
	}

	if err := bcrypt.CompareHashAndPassword([]byte(otpData.OTPHash), []byte(otp)); err != nil {
		otpData.Attempts++
		if s.otpRepo != nil {
			_ = s.otpRepo.Store(ctx, phoneNumber, *otpData)
		}
		return false, op.Outcome("invalid", errInvalidOTP)
	}

	s.deleteLocalOTP(ctx, phoneNumber)
	op.With("outcome", "verified")
	return true, nil
}

func (s *OTPService) verifyViaTwilio(ctx context.Context, op *logging.Op, phoneNumber, otp string) (bool, error) {
	op.With("provider", "twilio")

	valid, err := s.twilio.CheckVerification(ctx, phoneNumber, otp)
	if err != nil {
		return false, op.Fail(err)
	}
	if !valid {
		return false, op.Outcome("invalid", errInvalidOTP)
	}

	op.With("outcome", "verified")
	return true, nil
}

// forceTwilio reads the kill-switch. On missing routing client or Get error,
// fail open to Twilio-only (safer than routing to AT with unknown config).
func (s *OTPService) forceTwilio(ctx context.Context) bool {
	if s.routing == nil {
		return true
	}
	cfg, err := s.routing.Get(ctx)
	if err != nil || cfg == nil {
		return true
	}
	return cfg.ForceTwilio
}

func (s *OTPService) atConfigured() bool {
	return s.at != nil && s.at.Configured()
}

func (s *OTPService) getLocalOTP(ctx context.Context, phoneNumber string) (*models.OTPData, error) {
	if s.otpRepo == nil {
		return nil, fmt.Errorf("OTP store not configured")
	}
	return s.otpRepo.Get(ctx, phoneNumber)
}

func (s *OTPService) deleteLocalOTP(ctx context.Context, phoneNumber string) {
	if s.otpRepo == nil {
		return
	}
	_ = s.otpRepo.Delete(ctx, phoneNumber)
}

func isOTPReusable(data *models.OTPData, now time.Time, maxAttempts int) bool {
	return data != nil &&
		data.OTP != "" &&
		now.Before(data.ExpiresAt) &&
		data.Attempts < maxAttempts
}

func generateRandomOTP(length int) (string, error) {
	if length <= 0 {
		length = 6
	}
	const digits = "0123456789"
	otp := make([]byte, length)
	for i := range otp {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		if err != nil {
			return "", err
		}
		otp[i] = digits[n.Int64()]
	}
	return string(otp), nil
}
