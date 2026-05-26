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
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{"op": "GenerateOTP", "phone": phoneNumber}).Info("service call start")

	// TODO: uncomment random OTP generation before production
	// otp, err := s.generateRandomOTP(s.cfg.Length)
	// if err != nil {
	// 	return "", fmt.Errorf("failed to generate OTP: %w", err)
	// }
	otp := "112233"

	hashedOTP, err := bcrypt.GenerateFromPassword([]byte(otp), bcrypt.DefaultCost)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GenerateOTP",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return "", fmt.Errorf("failed to hash OTP: %w", err)
	}

	otpData := models.OTPData{
		OTPHash:   string(hashedOTP),
		Phone:     phoneNumber,
		Attempts:  0,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(s.cfg.Expiry),
	}

	if err := s.otpRepo.Store(ctx, phoneNumber, otpData); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GenerateOTP",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return "", err
	}

	if err := s.otpRepo.StoreTestOTP(ctx, phoneNumber, otp, otpData.ExpiresAt); err != nil {
		log.WithError(err).Warn("Failed to store test OTP")
	}

	log.WithFields(logrus.Fields{
		"phone": phoneNumber,
		"otp":   otp,
	}).Info("OTP generated (logged for development)")

	log.WithFields(logrus.Fields{
		"op":          "GenerateOTP",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("service call done")
	return otp, nil
}

func (s *OTPService) VerifyOTP(ctx context.Context, phoneNumber, otp string) (bool, error) {
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{"op": "VerifyOTP", "phone": phoneNumber}).Info("service call start")

	otpData, err := s.otpRepo.Get(ctx, phoneNumber)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "VerifyOTP",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return false, err
	}

	if time.Now().After(otpData.ExpiresAt) {
		s.otpRepo.Delete(ctx, phoneNumber)
		log.WithFields(logrus.Fields{
			"op":          "VerifyOTP",
			"duration_ms": time.Since(start).Milliseconds(),
			"outcome":     "expired",
		}).Info("service call done")
		return false, fmt.Errorf("OTP expired")
	}

	if otpData.Attempts >= s.cfg.MaxAttempts {
		s.otpRepo.Delete(ctx, phoneNumber)
		log.WithFields(logrus.Fields{
			"op":          "VerifyOTP",
			"duration_ms": time.Since(start).Milliseconds(),
			"outcome":     "max_attempts",
		}).Info("service call done")
		return false, fmt.Errorf("maximum attempts exceeded")
	}

	err = bcrypt.CompareHashAndPassword([]byte(otpData.OTPHash), []byte(otp))
	if err != nil {
		otpData.Attempts++
		s.otpRepo.Store(ctx, phoneNumber, *otpData)
		log.WithFields(logrus.Fields{
			"op":          "VerifyOTP",
			"duration_ms": time.Since(start).Milliseconds(),
			"outcome":     "invalid",
		}).Info("service call done")
		return false, fmt.Errorf("invalid OTP")
	}

	s.otpRepo.Delete(ctx, phoneNumber)
	log.WithFields(logrus.Fields{
		"op":          "VerifyOTP",
		"duration_ms": time.Since(start).Milliseconds(),
		"outcome":     "verified",
	}).Info("service call done")
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
