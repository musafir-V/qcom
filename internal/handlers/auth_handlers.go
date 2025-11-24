package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type AuthHandlers struct {
	otpService          *service.OTPService
	jwtService          *service.JWTService
	refreshTokenService *service.RefreshTokenService
	userRepo            *repository.UserRepository
	logger              *logrus.Logger
}

func NewAuthHandlers(
	otpService *service.OTPService,
	jwtService *service.JWTService,
	refreshTokenService *service.RefreshTokenService,
	userRepo *repository.UserRepository,
	logger *logrus.Logger,
) *AuthHandlers {
	return &AuthHandlers{
		otpService:          otpService,
		jwtService:          jwtService,
		refreshTokenService: refreshTokenService,
		userRepo:            userRepo,
		logger:              logger,
	}
}

type InitiateOTPRequest struct {
	PhoneNumber string `json:"phone_number"`
}

type InitiateOTPResponse struct {
	Message string `json:"message"`
}

type VerifyOTPRequest struct {
	PhoneNumber string `json:"phone_number"`
	OTP         string `json:"otp"`
}

type VerifyOTPResponse struct {
	AccessToken  string       `json:"access_token"`
	RefreshToken string       `json:"refresh_token"`
	TokenType    string       `json:"token_type"`
	ExpiresIn    int64        `json:"expires_in"`
	User         UserResponse `json:"user"`
}

type UserResponse struct {
	PhoneNumber string `json:"phone_number"`
	Name        string `json:"name,omitempty"`
}

type RefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type RefreshTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (h *AuthHandlers) InitiateOTP(w http.ResponseWriter, r *http.Request) {
	var req InitiateOTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.WithError(err).Error("Failed to generate OTP")
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	// Validate phone number
	phoneNumber := strings.TrimSpace(req.PhoneNumber)
	if !isValidPhoneNumber(phoneNumber) {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_PHONE", "Invalid phone number format")
		return
	}

	// Normalize phone number (ensure it starts with +)
	if !strings.HasPrefix(phoneNumber, "+") {
		phoneNumber = "+" + phoneNumber
	}

	// Generate and store OTP
	_, err := h.otpService.GenerateOTP(phoneNumber)
	if err != nil {
		h.logger.WithError(err).Error("Failed to generate OTP")
		h.respondWithError(w, http.StatusInternalServerError, "OTP_GENERATION_FAILED", "Failed to generate OTP")
		return
	}

	// OTP is logged in the service (for development)
	// In production, send via WhatsApp here

	h.respondWithJSON(w, http.StatusOK, InitiateOTPResponse{
		Message: "OTP sent successfully",
	})
}

func (h *AuthHandlers) VerifyOTP(w http.ResponseWriter, r *http.Request) {
	var req VerifyOTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	phoneNumber := strings.TrimSpace(req.PhoneNumber)
	otp := strings.TrimSpace(req.OTP)

	// Normalize phone number
	if !strings.HasPrefix(phoneNumber, "+") {
		phoneNumber = "+" + phoneNumber
	}

	// Validate inputs
	if !isValidPhoneNumber(phoneNumber) {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_PHONE", "Invalid phone number format")
		return
	}

	if len(otp) < 4 || len(otp) > 8 {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_OTP", "Invalid OTP format")
		return
	}

	// Verify OTP
	valid, err := h.otpService.VerifyOTP(phoneNumber, otp)
	if err != nil || !valid {
		h.respondWithError(w, http.StatusUnauthorized, "INVALID_OTP", "Invalid or expired OTP")
		return
	}

	// Get or create user
	user, err := h.userRepo.GetOrCreate(r.Context(), phoneNumber)
	if err != nil {
		h.logger.WithError(err).Error("Failed to get or create user")
		h.respondWithError(w, http.StatusInternalServerError, "USER_CREATION_FAILED", "Failed to create user")
		return
	}

	// Generate JWT tokens
	tokenPair, familyID, err := h.jwtService.GenerateAccessToken(phoneNumber)
	if err != nil {
		h.logger.WithError(err).Error("Failed to generate tokens")
		h.respondWithError(w, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "Failed to generate tokens")
		return
	}

	// Extract JTI from refresh token to store it
	claims, err := h.jwtService.VerifyToken(tokenPair.RefreshToken)
	if err != nil {
		h.logger.WithError(err).Error("Failed to verify refresh token")
		h.respondWithError(w, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "Failed to generate tokens")
		return
	}

	// Store refresh token
	if err := h.refreshTokenService.Store(
		r.Context(),
		claims.JTI,
		phoneNumber,
		phoneNumber,
		familyID,
		claims.RegisteredClaims.ExpiresAt.Time,
	); err != nil {
		h.logger.WithError(err).Error("Failed to store refresh token")
		// Continue anyway, token is still valid
	}

	h.respondWithJSON(w, http.StatusOK, VerifyOTPResponse{
		AccessToken:  tokenPair.AccessToken,
		RefreshToken: tokenPair.RefreshToken,
		TokenType:    tokenPair.TokenType,
		ExpiresIn:    tokenPair.ExpiresIn,
		User: UserResponse{
			PhoneNumber: user.PhoneNumber,
			Name:        user.Name,
		},
	})
}

func (h *AuthHandlers) RefreshToken(w http.ResponseWriter, r *http.Request) {
	var req RefreshTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	if req.RefreshToken == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_TOKEN", "Refresh token is required")
		return
	}

	// Verify refresh token
	claims, err := h.jwtService.VerifyToken(req.RefreshToken)
	if err != nil {
		h.respondWithError(w, http.StatusUnauthorized, "INVALID_TOKEN", "Invalid refresh token")
		return
	}

	if claims.Type != "refresh" {
		h.respondWithError(w, http.StatusUnauthorized, "INVALID_TOKEN_TYPE", "Token is not a refresh token")
		return
	}

	// Check if token is revoked
	revoked, err := h.refreshTokenService.IsRevoked(r.Context(), claims.JTI)
	if err == nil && revoked {
		h.respondWithError(w, http.StatusUnauthorized, "TOKEN_REVOKED", "Refresh token has been revoked")
		return
	}

	// Get token data to get family ID
	tokenData, err := h.refreshTokenService.Get(r.Context(), claims.JTI)
	if err != nil {
		h.logger.WithError(err).Warn("Failed to get refresh token data, will generate new family ID")
	}

	// Revoke old refresh token
	if tokenData != nil {
		h.refreshTokenService.Revoke(r.Context(), claims.JTI)
	}

	// Get family ID from existing token or use empty string (will generate new)
	familyID := ""
	if tokenData != nil {
		familyID = tokenData.FamilyID
	}

	// Generate new tokens with same family ID
	newTokenPair, newFamilyID, err := h.jwtService.RefreshTokens(req.RefreshToken, familyID)
	if err != nil {
		h.logger.WithError(err).Error("Failed to generate new tokens")
		h.respondWithError(w, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "Failed to generate tokens")
		return
	}

	// Extract JTI from new refresh token
	newClaims, err := h.jwtService.VerifyToken(newTokenPair.RefreshToken)
	if err != nil {
		h.logger.WithError(err).Error("Failed to verify new refresh token")
		h.respondWithError(w, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "Failed to generate tokens")
		return
	}

	// Store new refresh token with family ID
	if err := h.refreshTokenService.Store(
		r.Context(),
		newClaims.JTI,
		claims.Phone,
		claims.Phone,
		newFamilyID,
		newClaims.RegisteredClaims.ExpiresAt.Time,
	); err != nil {
		h.logger.WithError(err).Error("Failed to store new refresh token")
		// Continue anyway
	}

	h.respondWithJSON(w, http.StatusOK, RefreshTokenResponse{
		AccessToken:  newTokenPair.AccessToken,
		RefreshToken: newTokenPair.RefreshToken,
		TokenType:    newTokenPair.TokenType,
		ExpiresIn:    newTokenPair.ExpiresIn,
	})
}

func (h *AuthHandlers) Logout(w http.ResponseWriter, r *http.Request) {
	// Get token from context (set by auth middleware)
	_, ok := r.Context().Value("claims").(*service.Claims)
	if !ok {
		h.respondWithError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid token")
		return
	}

	// Extract refresh token from request body (optional)
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	// If refresh token provided, revoke it
	if req.RefreshToken != "" {
		refreshClaims, err := h.jwtService.VerifyToken(req.RefreshToken)
		if err == nil && refreshClaims.Type == "refresh" {
			h.refreshTokenService.Revoke(r.Context(), refreshClaims.JTI)
		}
	}

	h.respondWithJSON(w, http.StatusOK, map[string]string{
		"message": "Logged out successfully",
	})
}

func (h *AuthHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func (h *AuthHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{
		Error: ErrorDetail{
			Code:    code,
			Message: message,
		},
	})
}

func isValidPhoneNumber(phone string) bool {
	// E.164 format: +[country code][number] (max 15 digits after +)
	matched, _ := regexp.MatchString(`^\+[1-9]\d{1,14}$`, phone)
	return matched
}
