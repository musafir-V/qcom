package service

import (
	"context"

	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

const masterOTPBypass = "221133"

type twilioOTPVerifier interface {
	StartSMSVerification(ctx context.Context, phoneNumber string) error
	CheckVerification(ctx context.Context, phoneNumber, code string) (bool, error)
}

type OTPService struct {
	twilio twilioOTPVerifier
	logger *logrus.Logger
}

func NewOTPService(twilio *TwilioVerifyService, logger *logrus.Logger) *OTPService {
	return &OTPService{
		twilio: twilio,
		logger: logger,
	}
}

// GenerateOTP starts a Twilio Verify SMS to phoneNumber.
// On Twilio failure it still returns success so clients can use masterOTPBypass.
func (s *OTPService) GenerateOTP(ctx context.Context, phoneNumber string) (string, error) {
	op := logging.Start(ctx, s.logger, "GenerateOTP", logrus.Fields{"phone": phoneNumber})
	defer op.End()

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
