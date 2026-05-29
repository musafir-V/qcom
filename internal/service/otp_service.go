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
	otpRepo *repository.OTPRepository
	cfg     *config.OTPConfig
	logger  *logrus.Logger
}

func NewOTPService(otpRepo *repository.OTPRepository, cfg *config.OTPConfig, logger *logrus.Logger) *OTPService {
	return &OTPService{
		otpRepo: otpRepo,
		cfg:     cfg,
		logger:  logger,
	}
}

func (s *OTPService) GenerateOTP(ctx context.Context, phoneNumber string) (string, error) {
	op := logging.Start(ctx, s.logger, "GenerateOTP", logrus.Fields{"phone": phoneNumber})
	defer op.End()

	// TODO: uncomment random OTP generation before production
	// otp, err := s.generateRandomOTP(s.cfg.Length)
	// if err != nil {
	// 	return "", fmt.Errorf("failed to generate OTP: %w", err)
	// }
	otp := "112233"

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

	if err := s.otpRepo.StoreTestOTP(ctx, phoneNumber, otp, otpData.ExpiresAt); err != nil {
		op.Logger().WithError(err).Warn("Failed to store test OTP")
	}

	op.Logger().WithFields(logrus.Fields{
		"phone": phoneNumber,
		"otp":   otp,
	}).Info("OTP generated (logged for development)")
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

func (s *OTPService) generateRandomOTP(length int) (string, error) {
	otp := ""
	for i := 0; i < length; i++ {
		num, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		otp += num.String()
	}
	return otp, nil
}
