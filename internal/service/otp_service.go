package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/models"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

type OTPService struct {
	redis  *redis.Client
	cfg    *config.OTPConfig
	logger *logrus.Logger
}

func NewOTPService(client *redis.Client, cfg *config.OTPConfig, logger *logrus.Logger) *OTPService {
	return &OTPService{
		redis:  client,
		cfg:    cfg,
		logger: logger,
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

	// Store OTP data in Redis
	otpData := models.OTPData{
		OTPHash:   string(hashedOTP),
		Phone:     phoneNumber,
		Attempts:  0,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(s.cfg.Expiry),
	}

	dataJSON, err := json.Marshal(otpData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal OTP data: %w", err)
	}

	key := fmt.Sprintf("otp:%s", phoneNumber)
	ttl := s.cfg.Expiry

	// Store OTP data in Redis/Valkey
	ctx := context.Background()
	if err := s.redis.Set(ctx, key, dataJSON, ttl).Err(); err != nil {
		s.logger.WithError(err).Error("Failed to store OTP in Redis/Valkey")
		return "", fmt.Errorf("failed to store OTP: %w", err)
	}

	// Store plain OTP in test key (for integration tests only)
	// This allows tests to retrieve OTP without hashing
	testKey := fmt.Sprintf("otp:plain:%s", phoneNumber)
	s.redis.Set(ctx, testKey, otp, ttl)

	// Log OTP (for development - remove in production)
	s.logger.WithFields(logrus.Fields{
		"phone": phoneNumber,
		"otp":   otp,
	}).Info("OTP generated (logged for development)")

	return otp, nil
}

func (s *OTPService) VerifyOTP(phoneNumber, otp string) (bool, error) {
	ctx := context.Background()
	key := fmt.Sprintf("otp:%s", phoneNumber)

	// Get OTP data from Redis/Valkey
	dataJSON, err := s.redis.Get(ctx, key).Result()
	if err == redis.Nil {
		return false, fmt.Errorf("OTP not found or expired")
	}
	if err != nil {
		s.logger.WithError(err).Error("Failed to get OTP from Redis/Valkey")
		return false, fmt.Errorf("failed to get OTP: %w", err)
	}

	var otpData models.OTPData
	if err := json.Unmarshal([]byte(dataJSON), &otpData); err != nil {
		return false, fmt.Errorf("failed to unmarshal OTP data: %w", err)
	}

	// Check if expired
	if time.Now().After(otpData.ExpiresAt) {
		// Delete expired OTP
		s.redis.Del(ctx, key)
		return false, fmt.Errorf("OTP expired")
	}

	// Check attempts
	if otpData.Attempts >= s.cfg.MaxAttempts {
		// Delete OTP after max attempts
		s.redis.Del(ctx, key)
		return false, fmt.Errorf("maximum attempts exceeded")
	}

	// Verify OTP
	err = bcrypt.CompareHashAndPassword([]byte(otpData.OTPHash), []byte(otp))
	if err != nil {
		// Increment attempts
		otpData.Attempts++
		updatedJSON, _ := json.Marshal(otpData)
		s.redis.Set(ctx, key, updatedJSON, time.Until(otpData.ExpiresAt))
		return false, fmt.Errorf("invalid OTP")
	}

	// OTP verified successfully, delete it
	s.redis.Del(ctx, key)
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
