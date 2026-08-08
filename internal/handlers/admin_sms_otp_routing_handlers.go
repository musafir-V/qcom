package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

// smsOTPRoutingConfigStore is the slice of SMSOTPRoutingConfigRepository this
// handler needs, declared as an interface so tests can inject a stub.
type smsOTPRoutingConfigStore interface {
	Get(ctx context.Context) (*models.SMSOTPRoutingConfig, error)
	SetForceTwilio(ctx context.Context, force bool) error
}

// AdminSMSOTPRoutingHandlers powers the SMS OTP routing kill-switch admin API.
// Mount behind RequireAdminAuth (parent router applies auth).
type AdminSMSOTPRoutingHandlers struct {
	repo   smsOTPRoutingConfigStore
	logger *logrus.Logger
}

func NewAdminSMSOTPRoutingHandlers(repo *repository.SMSOTPRoutingConfigRepository, logger *logrus.Logger) *AdminSMSOTPRoutingHandlers {
	return &AdminSMSOTPRoutingHandlers{repo: repo, logger: logger}
}

// newAdminSMSOTPRoutingHandlers is the test-friendly constructor.
func newAdminSMSOTPRoutingHandlers(repo smsOTPRoutingConfigStore, logger *logrus.Logger) *AdminSMSOTPRoutingHandlers {
	return &AdminSMSOTPRoutingHandlers{repo: repo, logger: logger}
}

type smsOTPRoutingResponse struct {
	ForceTwilio bool `json:"force_twilio"`
}

// GetConfig handles GET /sms-otp-routing → {"force_twilio": bool}
func (h *AdminSMSOTPRoutingHandlers) GetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.repo.Get(r.Context())
	if err != nil {
		h.logger.WithError(err).Error("sms otp routing: get failed")
		h.respondWithError(w, http.StatusInternalServerError, "CONFIG_FETCH_FAILED", "Failed to load SMS OTP routing config")
		return
	}
	h.respondWithJSON(w, http.StatusOK, smsOTPRoutingResponse{ForceTwilio: cfg.ForceTwilio})
}

// PutConfig handles PUT /sms-otp-routing body {"force_twilio": bool} → same response.
func (h *AdminSMSOTPRoutingHandlers) PutConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ForceTwilio *bool `json:"force_twilio"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ForceTwilio == nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "force_twilio boolean is required")
		return
	}
	if err := h.repo.SetForceTwilio(r.Context(), *req.ForceTwilio); err != nil {
		h.logger.WithError(err).Error("sms otp routing: set failed")
		h.respondWithError(w, http.StatusInternalServerError, "CONFIG_UPDATE_FAILED", "Failed to update SMS OTP routing config")
		return
	}
	h.respondWithJSON(w, http.StatusOK, smsOTPRoutingResponse{ForceTwilio: *req.ForceTwilio})
}

func (h *AdminSMSOTPRoutingHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (h *AdminSMSOTPRoutingHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{Error: ErrorDetail{Code: code, Message: message}})
}
