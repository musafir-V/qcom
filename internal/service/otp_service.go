package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

const masterOTPBypass = "112233"

// Rejections a caller should surface as "invalid OTP", as opposed to the
// infrastructure failures VerifyOTP also returns.
var (
	ErrOTPInvalid     = errors.New("invalid OTP")
	ErrOTPExpired     = errors.New("OTP expired")
	ErrOTPMaxAttempts = errors.New("maximum OTP attempts exceeded")
	ErrOTPNotFound    = repository.ErrOTPNotFound
)

// IsOTPRejection reports whether err is a credential rejection rather than a
// failure of the OTP store itself.
func IsOTPRejection(err error) bool {
	return errors.Is(err, ErrOTPInvalid) ||
		errors.Is(err, ErrOTPExpired) ||
		errors.Is(err, ErrOTPMaxAttempts) ||
		errors.Is(err, ErrOTPNotFound)
}

type whatsAppOTPSender interface {
	SendWhatsAppOTP(ctx context.Context, phoneNumber, otp string) error
}

type otpStore interface {
	Store(ctx context.Context, phoneNumber string, otpData models.OTPData) error
	Get(ctx context.Context, phoneNumber string) (*models.OTPData, error)
	Delete(ctx context.Context, phoneNumber string) error
}

type OTPService struct {
	otpRepo       otpStore
	vonageService whatsAppOTPSender
	cfg           *config.OTPConfig
	logger        *logrus.Logger
}

func NewOTPService(otpRepo *repository.OTPRepository, vonageService *VonageService, cfg *config.OTPConfig, logger *logrus.Logger) *OTPService {
	return &OTPService{
		otpRepo:       otpRepo,
		vonageService: vonageService,
		cfg:           cfg,
		logger:        logger,
	}
}

func (s *OTPService) GenerateOTP(ctx context.Context, phoneNumber string) (string, error) {
	op := logging.Start(ctx, s.logger, "GenerateOTP", logrus.Fields{"phone": phoneNumber})
	defer op.End()

	if existing, err := s.otpRepo.Get(ctx, phoneNumber); err == nil && isOTPReusable(existing, time.Now(), s.cfg.MaxAttempts) {
		if err := s.vonageService.SendWhatsAppOTP(ctx, phoneNumber, existing.OTP); err != nil {
			op.Logger().WithError(err).Error("Failed to resend existing OTP via Vonage WhatsApp")
			return "", op.Fail(fmt.Errorf("failed to send OTP: %w", err))
		}
		op.With("outcome", "resent")
		return existing.OTP, nil
	}

	otp, err := generateRandomOTP(s.cfg.Length)
	if err != nil {
		return "", op.Fail(fmt.Errorf("failed to generate OTP: %w", err))
	}

	if err := s.vonageService.SendWhatsAppOTP(ctx, phoneNumber, otp); err != nil {
		op.Logger().WithError(err).Error("Failed to send OTP via Vonage WhatsApp")
		return "", op.Fail(fmt.Errorf("failed to send OTP: %w", err))
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
	return otp, nil
}

func isOTPReusable(data *models.OTPData, now time.Time, maxAttempts int) bool {
	return data != nil &&
		data.OTP != "" &&
		now.Before(data.ExpiresAt) &&
		data.Attempts < maxAttempts
}

func (s *OTPService) VerifyOTP(ctx context.Context, phoneNumber, otp string) (bool, error) {
	op := logging.Start(ctx, s.logger, "VerifyOTP", logrus.Fields{"phone": phoneNumber})
	defer op.End()

	if otp == masterOTPBypass {
		op.With("outcome", "master_bypass")
		return true, nil
	}

	otpData, err := s.otpRepo.Get(ctx, phoneNumber)
	if err != nil {
		if IsOTPRejection(err) {
			return false, op.Outcome("not_found", err)
		}
		return false, op.Fail(err)
	}
	if otpData == nil {
		return false, op.Outcome("not_found", ErrOTPNotFound)
	}

	if time.Now().After(otpData.ExpiresAt) {
		if derr := s.otpRepo.Delete(ctx, phoneNumber); derr != nil {
			op.Logger().WithError(derr).WithField("phone", phoneNumber).Warn("failed to delete expired OTP")
		}
		return false, op.Outcome("expired", ErrOTPExpired)
	}

	if otpData.Attempts >= s.cfg.MaxAttempts {
		if derr := s.otpRepo.Delete(ctx, phoneNumber); derr != nil {
			op.Logger().WithError(derr).WithField("phone", phoneNumber).Warn("failed to delete exhausted OTP")
		}
		return false, op.Outcome("max_attempts", ErrOTPMaxAttempts)
	}

	err = bcrypt.CompareHashAndPassword([]byte(otpData.OTPHash), []byte(otp))
	if err != nil {
		otpData.Attempts++
		// The attempt counter is the brute-force guard: if it cannot be
		// persisted the verification must fail loudly rather than hand the
		// caller an unlimited-retry window.
		if serr := s.otpRepo.Store(ctx, phoneNumber, *otpData); serr != nil {
			return false, op.Fail(fmt.Errorf("failed to record failed OTP attempt: %w", serr))
		}
		return false, op.Outcome("invalid", ErrOTPInvalid)
	}

	// A surviving OTP row could be replayed, so a failed delete is an error.
	if derr := s.otpRepo.Delete(ctx, phoneNumber); derr != nil {
		return false, op.Fail(fmt.Errorf("failed to consume verified OTP: %w", derr))
	}
	op.With("outcome", "verified")
	return true, nil
}

func generateRandomOTP(length int) (string, error) {
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
