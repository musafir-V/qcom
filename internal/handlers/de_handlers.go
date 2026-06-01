package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type DEHandlers struct {
	deService *service.DEService
	qrService *service.QRService
	logger    *logrus.Logger
}

func NewDEHandlers(deService *service.DEService, qrService *service.QRService, logger *logrus.Logger) *DEHandlers {
	return &DEHandlers{deService: deService, qrService: qrService, logger: logger}
}

// POST /api/v1/de/register
func (h *DEHandlers) Register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PhoneNumber  string `json:"phone_number"`
		Name         string `json:"name"`
		ProfileURL   string `json:"profile_url"`
		NRCURL       string `json:"nrc_url"`
		ReferralCode string `json:"referral_code"` // optional
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	req.PhoneNumber = strings.TrimSpace(req.PhoneNumber)
	if !strings.HasPrefix(req.PhoneNumber, "+") {
		req.PhoneNumber = "+" + req.PhoneNumber
	}
	if !isValidPhoneNumber(req.PhoneNumber) {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_PHONE", "Invalid phone number format")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "name is required")
		return
	}
	if strings.TrimSpace(req.ProfileURL) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "profile_url is required")
		return
	}
	if strings.TrimSpace(req.NRCURL) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "nrc_url is required")
		return
	}

	de, err := h.deService.Register(r.Context(), service.RegisterDERequest{
		PhoneNumber:  req.PhoneNumber,
		Name:         req.Name,
		ProfileURL:   req.ProfileURL,
		NRCURL:       req.NRCURL,
		ReferralCode: req.ReferralCode,
	})
	if err != nil {
		if strings.Contains(err.Error(), "already registered") {
			h.respondWithError(w, http.StatusConflict, "DE_ALREADY_EXISTS", err.Error())
			return
		}
		h.logger.WithError(err).Error("Failed to register DE")
		h.respondWithError(w, http.StatusInternalServerError, "REGISTRATION_FAILED", "Failed to register delivery executive")
		return
	}

	h.respondWithJSON(w, http.StatusCreated, map[string]interface{}{
		"de_id":         de.DEID,
		"phone_number":  de.PhoneNumber,
		"name":          de.Name,
		"status":        de.Status,
		"referral_code": de.ReferralCode,
		"created_at":    de.CreatedAt,
	})
}

// GET /api/v1/de/me
func (h *DEHandlers) GetMe(w http.ResponseWriter, r *http.Request) {
	phone, _ := r.Context().Value("phone").(string)

	de, err := h.deService.GetDE(r.Context(), phone)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, "DE_NOT_FOUND", "Delivery executive not found")
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"de_id":            de.DEID,
		"phone_number":     de.PhoneNumber,
		"name":             de.Name,
		"profile_url":      de.ProfileURL,
		"status":           de.Status,
		"current_store_id": de.CurrentStoreID,
		"current_order_id": de.CurrentOrderID,
		"created_at":       de.CreatedAt,
	})
}

// POST /api/v1/de/duty/start
func (h *DEHandlers) StartDuty(w http.ResponseWriter, r *http.Request) {
	phone, _ := r.Context().Value("phone").(string)

	var req struct {
		QRCode string `json:"qr_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if strings.TrimSpace(req.QRCode) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "qr_code is required")
		return
	}

	storeID, err := h.deService.StartDuty(r.Context(), phone, req.QRCode)
	if err != nil {
		status := http.StatusBadRequest
		code := "DUTY_START_FAILED"
		if strings.Contains(err.Error(), "expired") {
			code = "QR_EXPIRED"
		} else if strings.Contains(err.Error(), "active delivery") || strings.Contains(err.Error(), "already on duty") {
			code = "INVALID_STATE"
		}
		h.respondWithError(w, status, code, err.Error())
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "eligible",
		"store_id": storeID,
		"message":  "Duty started. You are now eligible for order assignment.",
	})
}

// GET /api/v1/stores/{storeId}/qr
func (h *DEHandlers) GetStoreQR(w http.ResponseWriter, r *http.Request) {
	storeID := mux.Vars(r)["storeId"]
	if storeID == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "storeId is required")
		return
	}

	qrCode := h.qrService.GenerateQRCode(storeID)
	validUntil := h.qrService.ValidUntil()

	h.respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"store_id":    storeID,
		"qr_code":     qrCode,
		"valid_until": validUntil.Format("2006-01-02T15:04:05Z07:00"),
	})
}

// POST /api/v1/de/duty/end
func (h *DEHandlers) EndDuty(w http.ResponseWriter, r *http.Request) {
	phone, _ := r.Context().Value("phone").(string)

	if err := h.deService.EndDuty(r.Context(), phone); err != nil {
		errStr := err.Error()
		code := "DUTY_END_FAILED"
		status := http.StatusBadRequest
		if strings.Contains(errStr, "active delivery") {
			code = "ACTIVE_DELIVERY"
		} else if strings.Contains(errStr, "already offline") {
			code = "ALREADY_OFFLINE"
		}
		h.respondWithError(w, status, code, errStr)
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]string{
		"status":  "offline",
		"message": "Duty ended.",
	})
}

func (h *DEHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func (h *DEHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{
		Error: ErrorDetail{Code: code, Message: message},
	})
}
