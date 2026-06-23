package service

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type JWTService struct {
	secretKey     []byte
	accessExpiry  time.Duration
	refreshExpiry time.Duration
	logger        *logrus.Logger
}

func NewJWTService(cfg *config.JWTConfig, logger *logrus.Logger) (*JWTService, error) {
	secretKey := []byte(cfg.SecretKey)
	if len(secretKey) < 32 {
		return nil, fmt.Errorf("secret key must be at least 32 bytes")
	}

	return &JWTService{
		secretKey:     secretKey,
		accessExpiry:  cfg.AccessExpiry,
		refreshExpiry: cfg.RefreshExpiry,
		logger:        logger,
	}, nil
}

type Claims struct {
	Phone      string `json:"phone"`
	EntityID   string `json:"entity_id"`
	EntityType string `json:"entity_type"` // "customer" | "de"
	Type       string `json:"type"`
	JTI        string `json:"jti"`
	jwt.RegisteredClaims
}

func (s *JWTService) GenerateAccessToken(phoneNumber, entityID, entityType string) (*models.TokenPair, string, error) {
	now := time.Now()
	accessJTI := uuid.New().String()
	refreshJTI := uuid.New().String()
	familyID := uuid.New().String()

	accessClaims := &Claims{
		Phone:      phoneNumber,
		EntityID:   entityID,
		EntityType: entityType,
		Type:       "access",
		JTI:        accessJTI,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   entityID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessExpiry)),
			ID:        accessJTI,
		},
	}

	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessTokenString, err := accessToken.SignedString(s.secretKey)
	if err != nil {
		s.logger.WithError(err).Error("Failed to sign access token")
		return nil, "", fmt.Errorf("failed to sign access token: %w", err)
	}

	refreshClaims := &Claims{
		Phone:      phoneNumber,
		EntityID:   entityID,
		EntityType: entityType,
		Type:       "refresh",
		JTI:        refreshJTI,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   entityID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.refreshExpiry)),
			ID:        refreshJTI,
		},
	}

	refreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshTokenString, err := refreshToken.SignedString(s.secretKey)
	if err != nil {
		s.logger.WithError(err).Error("Failed to sign refresh token")
		return nil, "", fmt.Errorf("failed to sign refresh token: %w", err)
	}

	return &models.TokenPair{
		AccessToken:  accessTokenString,
		RefreshToken: refreshTokenString,
		TokenType:    "Bearer",
		ExpiresIn:    int64(s.accessExpiry.Seconds()),
	}, familyID, nil
}

func (s *JWTService) VerifyToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.secretKey, nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	return claims, nil
}

func (s *JWTService) RefreshTokens(refreshTokenString string, familyID string) (*models.TokenPair, string, error) {
	claims, err := s.VerifyToken(refreshTokenString)
	if err != nil {
		return nil, "", fmt.Errorf("invalid refresh token: %w", err)
	}

	if claims.Type != "refresh" {
		return nil, "", fmt.Errorf("token is not a refresh token")
	}

	return s.GenerateAccessTokenWithFamily(claims.Phone, claims.EntityID, claims.EntityType, familyID)
}

func (s *JWTService) GenerateAccessTokenWithFamily(phoneNumber, entityID, entityType, familyID string) (*models.TokenPair, string, error) {
	now := time.Now()
	accessJTI := uuid.New().String()
	refreshJTI := uuid.New().String()

	if familyID == "" {
		familyID = uuid.New().String()
	}

	accessClaims := &Claims{
		Phone:      phoneNumber,
		EntityID:   entityID,
		EntityType: entityType,
		Type:       "access",
		JTI:        accessJTI,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   entityID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessExpiry)),
			ID:        accessJTI,
		},
	}

	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessTokenString, err := accessToken.SignedString(s.secretKey)
	if err != nil {
		s.logger.WithError(err).Error("Failed to sign access token")
		return nil, "", fmt.Errorf("failed to sign access token: %w", err)
	}

	refreshClaims := &Claims{
		Phone:      phoneNumber,
		EntityID:   entityID,
		EntityType: entityType,
		Type:       "refresh",
		JTI:        refreshJTI,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   entityID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.refreshExpiry)),
			ID:        refreshJTI,
		},
	}

	refreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshTokenString, err := refreshToken.SignedString(s.secretKey)
	if err != nil {
		s.logger.WithError(err).Error("Failed to sign refresh token")
		return nil, "", fmt.Errorf("failed to sign refresh token: %w", err)
	}

	return &models.TokenPair{
		AccessToken:  accessTokenString,
		RefreshToken: refreshTokenString,
		TokenType:    "Bearer",
		ExpiresIn:    int64(s.accessExpiry.Seconds()),
	}, familyID, nil
}

// adminTokenExpiry is how long an admin dashboard session token stays valid.
// Admin tokens are single (no refresh) — the dashboard re-authenticates with
// username/password when this expires.
const adminTokenExpiry = 12 * time.Hour

// GenerateAdminToken issues a signed access token for an admin dashboard user.
// The token carries entity_type="admin" so RequireAdminAuth can authorize it.
// Returns the token string and its lifetime in seconds.
func (s *JWTService) GenerateAdminToken(username string) (string, int64, error) {
	now := time.Now()
	jti := uuid.New().String()

	claims := &Claims{
		Phone:      username,
		EntityID:   username,
		EntityType: "admin",
		Type:       "access",
		JTI:        jti,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   username,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(adminTokenExpiry)),
			ID:        jti,
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.secretKey)
	if err != nil {
		s.logger.WithError(err).Error("Failed to sign admin token")
		return "", 0, fmt.Errorf("failed to sign admin token: %w", err)
	}
	return signed, int64(adminTokenExpiry.Seconds()), nil
}

func GenerateSecretKey() (string, error) {
	key := make([]byte, 32) // 256 bits
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(key), nil
}
