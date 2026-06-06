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
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

type OTPService struct {
	otpRepo       *repository.OTPRepository
	vonageService *VonageService
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

func (s *OTPService) VerifyOTP(ctx context.Context, phoneNumber, otp string) (bool, error) {
	op := logging.Start(ctx, s.logger, "VerifyOTP", logrus.Fields{"phone": phoneNumber})
	defer op.End()

	otpData, err := s.otpRepo.Get(ctx, phoneNumber)
	if err != nil {
		return false, op.Fail(err)
	}

	if time.Now().After(otpData.ExpiresAt) {
		s.otpRepo.Delete(ctx, phoneNumber)
		return false, op.Outcome("expired", fmt.Errorf("OTP expired"))
	}

	if otpData.Attempts >= s.cfg.MaxAttempts {
		s.otpRepo.Delete(ctx, phoneNumber)
		return false, op.Outcome("max_attempts", fmt.Errorf("maximum attempts exceeded"))
	}

	err = bcrypt.CompareHashAndPassword([]byte(otpData.OTPHash), []byte(otp))
	if err != nil {
		otpData.Attempts++
		s.otpRepo.Store(ctx, phoneNumber, *otpData)
		return false, op.Outcome("invalid", fmt.Errorf("invalid OTP"))
	}

	s.otpRepo.Delete(ctx, phoneNumber)
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
