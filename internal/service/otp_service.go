package service

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/qcom/qcom/internal/config"
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

func (s *OTPService) GenerateOTP(phoneNumber string) (string, error) {
	// Generate random OTP
	otp, err := s.generateRandomOTP(s.cfg.Length)
	if err != nil {
		return "", fmt.Errorf("failed to generate OTP: %w", err)
	}

	// Hash OTP before storing
	hashedOTP, err := bcrypt.GenerateFromPassword([]byte(otp), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash OTP: %w", err)
	}

	// Store OTP data in DynamoDB
	otpData := models.OTPData{
		OTPHash:   string(hashedOTP),
		Phone:     phoneNumber,
		Attempts:  0,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(s.cfg.Expiry),
	}

	ctx := context.Background()
	if err := s.otpRepo.Store(ctx, phoneNumber, otpData); err != nil {
		return "", err
	}

	// Store plain OTP for testing purposes
	if err := s.otpRepo.StoreTestOTP(ctx, phoneNumber, otp, otpData.ExpiresAt); err != nil {
		s.logger.WithError(err).Warn("Failed to store test OTP")
	}

	// Log OTP (for development - remove in production)
	s.logger.WithFields(logrus.Fields{
		"phone": phoneNumber,
		"otp":   otp,
	}).Info("OTP generated (logged for development)")

	return otp, nil
}

func (s *OTPService) VerifyOTP(phoneNumber, otp string) (bool, error) {
	ctx := context.Background()

	// Get OTP data from DynamoDB
	otpData, err := s.otpRepo.Get(ctx, phoneNumber)
	if err != nil {
		return false, err
	}

	// Check if expired
	if time.Now().After(otpData.ExpiresAt) {
		// Delete expired OTP
		s.otpRepo.Delete(ctx, phoneNumber)
		return false, fmt.Errorf("OTP expired")
	}

	// Check attempts
	if otpData.Attempts >= s.cfg.MaxAttempts {
		// Delete OTP after max attempts
		s.otpRepo.Delete(ctx, phoneNumber)
		return false, fmt.Errorf("maximum attempts exceeded")
	}

	// Verify OTP
	err = bcrypt.CompareHashAndPassword([]byte(otpData.OTPHash), []byte(otp))
	if err != nil {
		// Increment attempts
		otpData.Attempts++
		s.otpRepo.Store(ctx, phoneNumber, *otpData)
		return false, fmt.Errorf("invalid OTP")
	}

	// OTP verified successfully, delete it
	s.otpRepo.Delete(ctx, phoneNumber)
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
