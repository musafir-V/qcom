package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/service"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

type DEHandlers struct {
	deService        *service.DEService
	qrService        *service.QRService
	payoutConfigRepo *repository.PayoutConfigRepository
	cashConfigRepo   *repository.CashConfigRepository
	logger           *logrus.Logger
}

func NewDEHandlers(deService *service.DEService, qrService *service.QRService, payoutConfigRepo *repository.PayoutConfigRepository, cashConfigRepo *repository.CashConfigRepository, logger *logrus.Logger) *DEHandlers {
	return &DEHandlers{deService: deService, qrService: qrService, payoutConfigRepo: payoutConfigRepo, cashConfigRepo: cashConfigRepo, logger: logger}
}

// POST /api/v1/de/register
func (h *DEHandlers) Register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PhoneNumber      string `json:"phone_number"`
		Name             string `json:"name"`
		ProfileURL       string `json:"profile_url"`
		NRCURL           string `json:"nrc_url"`
		DriverLicenseURL string `json:"driver_license_url"` // optional
		ReferralCode     string `json:"referral_code"`      // optional
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
	if strings.TrimSpace(req.DriverLicenseURL) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "driver_license_url is required")
		return
	}

	de, err := h.deService.Register(r.Context(), service.RegisterDERequest{
		PhoneNumber:      req.PhoneNumber,
		Name:             req.Name,
		ProfileURL:       req.ProfileURL,
		NRCURL:           req.NRCURL,
		DriverLicenseURL: req.DriverLicenseURL,
		ReferralCode:     req.ReferralCode,
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

	todayEarnings, err := h.deService.GetTodayEarnings(r.Context(), de.DEID)
	if err != nil {
		h.logger.WithError(err).Warn("failed to compute today's earnings; defaulting to 0")
		todayEarnings = 0
	}

	tripsToday := de.TripsToday(timezone.DateString())
	resp := map[string]interface{}{
		"de_id":                 de.DEID,
		"phone_number":          de.PhoneNumber,
		"name":                  de.Name,
		"profile_url":           de.ProfileURL,
		"status":                de.Status,
		"current_store_id":      de.CurrentStoreID,
		"current_order_id":      de.CurrentOrderID,
		"created_at":            de.CreatedAt,
		"trips_today":           tripsToday,
		"total_trips_completed": de.TotalTripsCompleted,
		"today_earnings_zmw":    todayEarnings,
	}

	payoutCfg, err := h.payoutConfigRepo.Get(r.Context())
	if err != nil {
		h.logger.WithError(err).Warn("failed to fetch payout config for daily_milestone; omitting from response")
	} else {
		resp["daily_milestone"] = service.ComputeDailyMilestone(tripsToday, payoutCfg)
	}

	// Unlike daily_milestone (omitted on payout-config error), cash_blocked must
	// always be present so the app can render the cash-limit screen; default the limit on error.
	cashCfg, err := h.cashConfigRepo.Get(r.Context())
	if err != nil {
		h.logger.WithError(err).Warn("failed to fetch cash config; defaulting limit")
		cashCfg = &models.CashConfig{}
	}
	resp["cash_limit_zmw"] = cashCfg.EffectiveLimitZMW()
	resp["cash_blocked"] = de.CashExceeds(cashCfg.EffectiveLimitZMW())

	h.respondWithJSON(w, http.StatusOK, resp)
}

// POST /api/v1/de/duty/start
func (h *DEHandlers) StartDuty(w http.ResponseWriter, r *http.Request) {
	phone, _ := r.Context().Value("phone").(string)

	var req struct {
		QRCode    string  `json:"qr_code"`
		Lat       float64 `json:"lat"`
		Lng       float64 `json:"lng"`
		AccuracyM float64 `json:"accuracy_m"`
		IsMocked  bool    `json:"is_mocked"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if strings.TrimSpace(req.QRCode) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "qr_code is required")
		return
	}

	storeID, err := h.deService.StartDuty(r.Context(), phone, req.QRCode, service.ScanLocation{
		Lat:       req.Lat,
		Lng:       req.Lng,
		AccuracyM: req.AccuracyM,
		IsMocked:  req.IsMocked,
	})
	if err != nil {
		status := http.StatusBadRequest
		code := "DUTY_START_FAILED"
		errStr := err.Error()
		switch {
		case strings.Contains(errStr, "expired"):
			code = "QR_EXPIRED"
		case strings.Contains(errStr, "active delivery") || strings.Contains(errStr, "already on duty"):
			code = "INVALID_STATE"
		case strings.Contains(errStr, "mocked"):
			code = "INVALID_LOCATION"
		case strings.Contains(errStr, "accuracy"):
			code = "LOCATION_INACCURATE"
		case strings.Contains(errStr, "geofence"):
			code = "OUTSIDE_GEOFENCE"
		case strings.Contains(errStr, "in-hand cash limit"):
			status = http.StatusConflict
			code = "CASH_LIMIT_EXCEEDED"
		}
		h.respondWithError(w, status, code, errStr)
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
