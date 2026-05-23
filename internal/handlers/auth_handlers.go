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
	deRepo              *repository.DERepository
	logger              *logrus.Logger
}

func NewAuthHandlers(
	otpService *service.OTPService,
	jwtService *service.JWTService,
	refreshTokenService *service.RefreshTokenService,
	userRepo *repository.UserRepository,
	deRepo *repository.DERepository,
	logger *logrus.Logger,
) *AuthHandlers {
	return &AuthHandlers{
		otpService:          otpService,
		jwtService:          jwtService,
		refreshTokenService: refreshTokenService,
		userRepo:            userRepo,
		deRepo:              deRepo,
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

type UserResponse struct {
	UserID      string `json:"user_id"`
	PhoneNumber string `json:"phone_number"`
	Name        string `json:"name,omitempty"`
}

type DEResponse struct {
	DEID        string `json:"de_id"`
	PhoneNumber string `json:"phone_number"`
	Name        string `json:"name"`
	Status      string `json:"status"`
}

type VerifyOTPResponse struct {
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token"`
	TokenType    string      `json:"token_type"`
	ExpiresIn    int64       `json:"expires_in"`
	EntityType   string      `json:"entity_type"`
	Entity       interface{} `json:"entity"`
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

func appType(r *http.Request) string {
	t := strings.TrimSpace(r.Header.Get("X-App-Type"))
	if t == "de" {
		return "de"
	}
	return "customer"
}

func (h *AuthHandlers) InitiateOTP(w http.ResponseWriter, r *http.Request) {
	var req InitiateOTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.WithError(err).Error("Failed to decode OTP request")
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	phoneNumber := strings.TrimSpace(req.PhoneNumber)
	if !isValidPhoneNumber(phoneNumber) {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_PHONE", "Invalid phone number format")
		return
	}

	if !strings.HasPrefix(phoneNumber, "+") {
		phoneNumber = "+" + phoneNumber
	}

	// For DE login, the DE must be registered first
	if appType(r) == "de" {
		exists, err := h.deRepo.Exists(r.Context(), phoneNumber)
		if err != nil {
			h.logger.WithError(err).Error("Failed to check DE existence")
			h.respondWithError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to process request")
			return
		}
		if !exists {
			h.respondWithError(w, http.StatusNotFound, "DE_NOT_FOUND", "No delivery executive registered with this number")
			return
		}
	}

	if _, err := h.otpService.GenerateOTP(phoneNumber); err != nil {
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

	if !strings.HasPrefix(phoneNumber, "+") {
		phoneNumber = "+" + phoneNumber
	}

	if !isValidPhoneNumber(phoneNumber) {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_PHONE", "Invalid phone number format")
		return
	}

	if len(otp) < 4 || len(otp) > 8 {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_OTP", "Invalid OTP format")
		return
	}

	valid, err := h.otpService.VerifyOTP(phoneNumber, otp)
	if err != nil || !valid {
		h.respondWithError(w, http.StatusUnauthorized, "INVALID_OTP", "Invalid or expired OTP")
		return
	}

	if appType(r) == "de" {
		h.verifyOTPForDE(w, r, phoneNumber)
	} else {
		h.verifyOTPForCustomer(w, r, phoneNumber)
	}
}

func (h *AuthHandlers) verifyOTPForDE(w http.ResponseWriter, r *http.Request, phoneNumber string) {
	de, err := h.deRepo.GetByPhone(r.Context(), phoneNumber)
	if err != nil || de == nil {
		h.respondWithError(w, http.StatusNotFound, "DE_NOT_FOUND", "Delivery executive not found")
		return
	}

	tokenPair, familyID, err := h.jwtService.GenerateAccessToken(phoneNumber, de.DEID, "de")
	if err != nil {
		h.logger.WithError(err).Error("Failed to generate tokens for DE")
		h.respondWithError(w, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "Failed to generate tokens")
		return
	}

	claims, err := h.jwtService.VerifyToken(tokenPair.RefreshToken)
	if err != nil {
		h.logger.WithError(err).Error("Failed to verify DE refresh token")
		h.respondWithError(w, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "Failed to generate tokens")
		return
	}

	if err := h.refreshTokenService.Store(
		r.Context(),
		claims.JTI,
		de.DEID,
		"de",
		phoneNumber,
		familyID,
		claims.RegisteredClaims.ExpiresAt.Time,
	); err != nil {
		h.logger.WithError(err).Error("Failed to store DE refresh token")
	}

	h.respondWithJSON(w, http.StatusOK, VerifyOTPResponse{
		AccessToken:  tokenPair.AccessToken,
		RefreshToken: tokenPair.RefreshToken,
		TokenType:    tokenPair.TokenType,
		ExpiresIn:    tokenPair.ExpiresIn,
		EntityType:   "de",
		Entity: DEResponse{
			DEID:        de.DEID,
			PhoneNumber: de.PhoneNumber,
			Name:        de.Name,
			Status:      string(de.Status),
		},
	})
}

func (h *AuthHandlers) verifyOTPForCustomer(w http.ResponseWriter, r *http.Request, phoneNumber string) {
	user, err := h.userRepo.GetOrCreate(r.Context(), phoneNumber)
	if err != nil {
		h.logger.WithError(err).Error("Failed to get or create user")
		h.respondWithError(w, http.StatusInternalServerError, "USER_CREATION_FAILED", "Failed to create user")
		return
	}

	tokenPair, familyID, err := h.jwtService.GenerateAccessToken(phoneNumber, user.UserID, "customer")
	if err != nil {
		h.logger.WithError(err).Error("Failed to generate tokens")
		h.respondWithError(w, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "Failed to generate tokens")
		return
	}

	claims, err := h.jwtService.VerifyToken(tokenPair.RefreshToken)
	if err != nil {
		h.logger.WithError(err).Error("Failed to verify refresh token")
		h.respondWithError(w, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "Failed to generate tokens")
		return
	}

	if err := h.refreshTokenService.Store(
		r.Context(),
		claims.JTI,
		user.UserID,
		"customer",
		phoneNumber,
		familyID,
		claims.RegisteredClaims.ExpiresAt.Time,
	); err != nil {
		h.logger.WithError(err).Error("Failed to store refresh token")
	}

	h.respondWithJSON(w, http.StatusOK, VerifyOTPResponse{
		AccessToken:  tokenPair.AccessToken,
		RefreshToken: tokenPair.RefreshToken,
		TokenType:    tokenPair.TokenType,
		ExpiresIn:    tokenPair.ExpiresIn,
		EntityType:   "customer",
		Entity: UserResponse{
			UserID:      user.UserID,
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

	claims, err := h.jwtService.VerifyToken(req.RefreshToken)
	if err != nil {
		h.respondWithError(w, http.StatusUnauthorized, "INVALID_TOKEN", "Invalid refresh token")
		return
	}

	if claims.Type != "refresh" {
		h.respondWithError(w, http.StatusUnauthorized, "INVALID_TOKEN_TYPE", "Token is not a refresh token")
		return
	}

	revoked, err := h.refreshTokenService.IsRevoked(r.Context(), claims.JTI)
	if err == nil && revoked {
		h.respondWithError(w, http.StatusUnauthorized, "TOKEN_REVOKED", "Refresh token has been revoked")
		return
	}

	tokenData, err := h.refreshTokenService.Get(r.Context(), claims.JTI)
	if err != nil {
		h.logger.WithError(err).Warn("Failed to get refresh token data, will generate new family ID")
	}

	if tokenData != nil {
		h.refreshTokenService.Revoke(r.Context(), claims.JTI)
	}

	familyID := ""
	if tokenData != nil {
		familyID = tokenData.FamilyID
	}

	newTokenPair, newFamilyID, err := h.jwtService.RefreshTokens(req.RefreshToken, familyID)
	if err != nil {
		h.logger.WithError(err).Error("Failed to generate new tokens")
		h.respondWithError(w, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "Failed to generate tokens")
		return
	}

	newClaims, err := h.jwtService.VerifyToken(newTokenPair.RefreshToken)
	if err != nil {
		h.logger.WithError(err).Error("Failed to verify new refresh token")
		h.respondWithError(w, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "Failed to generate tokens")
		return
	}

	if err := h.refreshTokenService.Store(
		r.Context(),
		newClaims.JTI,
		claims.EntityID,
		claims.EntityType,
		claims.Phone,
		newFamilyID,
		newClaims.RegisteredClaims.ExpiresAt.Time,
	); err != nil {
		h.logger.WithError(err).Error("Failed to store new refresh token")
	}

	h.respondWithJSON(w, http.StatusOK, RefreshTokenResponse{
		AccessToken:  newTokenPair.AccessToken,
		RefreshToken: newTokenPair.RefreshToken,
		TokenType:    newTokenPair.TokenType,
		ExpiresIn:    newTokenPair.ExpiresIn,
	})
}

func (h *AuthHandlers) Logout(w http.ResponseWriter, r *http.Request) {
	_, ok := r.Context().Value("claims").(*service.Claims)
	if !ok {
		h.respondWithError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid token")
		return
	}

	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	json.NewDecoder(r.Body).Decode(&req)

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
	matched, _ := regexp.MatchString(`^\+[1-9]\d{1,14}$`, phone)
	return matched
}
