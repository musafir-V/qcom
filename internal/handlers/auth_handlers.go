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
		respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	phoneNumber := strings.TrimSpace(req.PhoneNumber)
	if !isValidPhoneNumber(phoneNumber) {
		respondWithError(w, http.StatusBadRequest, "INVALID_PHONE", "Invalid phone number format")
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
			respondWithError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to process request")
			return
		}
		if !exists {
			respondWithError(w, http.StatusNotFound, "DE_NOT_FOUND", "No delivery executive registered with this number")
			return
		}
	}

	if _, err := h.otpService.GenerateOTP(r.Context(), phoneNumber); err != nil {
		h.logger.WithError(err).Error("Failed to generate OTP")
		respondWithError(w, http.StatusInternalServerError, "OTP_GENERATION_FAILED", "Failed to generate OTP")
		return
	}

	respondWithJSON(w, http.StatusOK, InitiateOTPResponse{
		Message: "OTP sent successfully",
	})
}

func (h *AuthHandlers) VerifyOTP(w http.ResponseWriter, r *http.Request) {
	var req VerifyOTPRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	phoneNumber := strings.TrimSpace(req.PhoneNumber)
	otp := strings.TrimSpace(req.OTP)

	if !strings.HasPrefix(phoneNumber, "+") {
		phoneNumber = "+" + phoneNumber
	}

	if !isValidPhoneNumber(phoneNumber) {
		respondWithError(w, http.StatusBadRequest, "INVALID_PHONE", "Invalid phone number format")
		return
	}

	if len(otp) < 4 || len(otp) > 8 {
		respondWithError(w, http.StatusBadRequest, "INVALID_OTP", "Invalid OTP format")
		return
	}

	valid, err := h.otpService.VerifyOTP(r.Context(), phoneNumber, otp)
	if err != nil || !valid {
		respondWithError(w, http.StatusUnauthorized, "INVALID_OTP", "Invalid or expired OTP")
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
		respondWithError(w, http.StatusNotFound, "DE_NOT_FOUND", "Delivery executive not found")
		return
	}

	tokenPair, familyID, err := h.jwtService.GenerateAccessToken(phoneNumber, de.DEID, "de")
	if err != nil {
		h.logger.WithError(err).Error("Failed to generate tokens for DE")
		respondWithError(w, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "Failed to generate tokens")
		return
	}

	claims, err := h.jwtService.VerifyToken(tokenPair.RefreshToken)
	if err != nil {
		h.logger.WithError(err).Error("Failed to verify DE refresh token")
		respondWithError(w, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "Failed to generate tokens")
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

	respondWithJSON(w, http.StatusOK, VerifyOTPResponse{
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
		respondWithError(w, http.StatusInternalServerError, "USER_CREATION_FAILED", "Failed to create user")
		return
	}

	tokenPair, familyID, err := h.jwtService.GenerateAccessToken(phoneNumber, user.UserID, "customer")
	if err != nil {
		h.logger.WithError(err).Error("Failed to generate tokens")
		respondWithError(w, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "Failed to generate tokens")
		return
	}

	claims, err := h.jwtService.VerifyToken(tokenPair.RefreshToken)
	if err != nil {
		h.logger.WithError(err).Error("Failed to verify refresh token")
		respondWithError(w, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "Failed to generate tokens")
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

	respondWithJSON(w, http.StatusOK, VerifyOTPResponse{
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
	if !decodeJSONBody(w, r, &req) {
		return
	}

	if req.RefreshToken == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_TOKEN", "Refresh token is required")
		return
	}

	claims, err := h.jwtService.VerifyToken(req.RefreshToken)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "INVALID_TOKEN", "Invalid refresh token")
		return
	}

	if claims.Type != "refresh" {
		respondWithError(w, http.StatusUnauthorized, "INVALID_TOKEN_TYPE", "Token is not a refresh token")
		return
	}

	revoked, err := h.refreshTokenService.IsRevoked(r.Context(), claims.JTI)
	if err == nil && revoked {
		respondWithError(w, http.StatusUnauthorized, "TOKEN_REVOKED", "Refresh token has been revoked")
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
		respondWithError(w, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "Failed to generate tokens")
		return
	}

	newClaims, err := h.jwtService.VerifyToken(newTokenPair.RefreshToken)
	if err != nil {
		h.logger.WithError(err).Error("Failed to verify new refresh token")
		respondWithError(w, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "Failed to generate tokens")
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

	respondWithJSON(w, http.StatusOK, RefreshTokenResponse{
		AccessToken:  newTokenPair.AccessToken,
		RefreshToken: newTokenPair.RefreshToken,
		TokenType:    newTokenPair.TokenType,
		ExpiresIn:    newTokenPair.ExpiresIn,
	})
}

func (h *AuthHandlers) Logout(w http.ResponseWriter, r *http.Request) {
	_, ok := r.Context().Value("claims").(*service.Claims)
	if !ok {
		respondWithError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid token")
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

	respondWithJSON(w, http.StatusOK, map[string]string{
		"message": "Logged out successfully",
	})
}

type DeleteAccountRequest struct {
	UserID string `json:"user_id"`
}

// DeleteAccount removes the authenticated customer's account so the App Store
// "Delete Account" requirement (Guideline 5.1.1(v)) is satisfied. It deletes the
// USER!<phone> row and revokes every refresh token for the account. The next OTP
// login for the same number mints a brand-new userId via GetOrCreate.
//
// The target is always derived from the JWT claims — never from the request body.
// A body user_id, if present, must match the caller's own entity_id (defends
// against deleting someone else's account). Per-user data in other partitions
// (addresses, phone-keyed trips) is intentionally left dangling.
func (h *AuthHandlers) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	claims, ok := r.Context().Value("claims").(*service.Claims)
	if !ok {
		respondWithError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid token")
		return
	}

	// Customers only — DE accounts are operational and must not self-delete here.
	if claims.EntityType != "customer" {
		respondWithError(w, http.StatusForbidden, "FORBIDDEN", "Only customer accounts can be deleted")
		return
	}

	// The body is optional. If a user_id is supplied it must be the caller's own.
	var req DeleteAccountRequest
	json.NewDecoder(r.Body).Decode(&req)
	if req.UserID != "" && req.UserID != claims.EntityID {
		respondWithError(w, http.StatusForbidden, "FORBIDDEN", "Cannot delete a different account")
		return
	}

	if err := h.userRepo.DeleteByPhone(r.Context(), claims.Phone); err != nil {
		h.logger.WithError(err).Error("Failed to delete user account")
		respondWithError(w, http.StatusInternalServerError, "DELETE_FAILED", "Failed to delete account")
		return
	}

	if err := h.refreshTokenService.RevokeAllForEntity(r.Context(), claims.EntityID); err != nil {
		// The account row is already gone; log but don't fail the request.
		h.logger.WithError(err).Error("Failed to revoke refresh tokens on account deletion")
	}

	respondWithJSON(w, http.StatusOK, map[string]string{
		"message": "Account deleted successfully",
	})
}

func isValidPhoneNumber(phone string) bool {
	matched, _ := regexp.MatchString(`^\+[1-9]\d{1,14}$`, phone)
	return matched
}
