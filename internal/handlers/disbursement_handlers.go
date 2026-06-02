package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

type DisbursementHandlers struct {
	disbursementRepo *repository.DisbursementRepository
	deRepo           *repository.DERepository
	logger           *logrus.Logger
}

func NewDisbursementHandlers(
	disbursementRepo *repository.DisbursementRepository,
	deRepo *repository.DERepository,
	logger *logrus.Logger,
) *DisbursementHandlers {
	return &DisbursementHandlers{
		disbursementRepo: disbursementRepo,
		deRepo:           deRepo,
		logger:           logger,
	}
}

// POST /api/v1/de/{deId}/disbursement
// Internal ops endpoint (no auth). Records an offline payout to a DE and, when
// de_phone is supplied, advances the DE's last_disbursed_at watermark.
// Body: { "amount_zmw": 500.0, "period_from": "2026-05-01", "period_to": "2026-05-31", "de_phone": "+26097..." }
func (h *DisbursementHandlers) RecordDisbursement(w http.ResponseWriter, r *http.Request) {
	deID := mux.Vars(r)["deId"]
	if strings.TrimSpace(deID) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "deId is required")
		return
	}

	var req struct {
		AmountZMW  float64 `json:"amount_zmw"`
		PeriodFrom string  `json:"period_from"`
		PeriodTo   string  `json:"period_to"`
		DEPhone    string  `json:"de_phone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if req.AmountZMW <= 0 {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_AMOUNT", "amount_zmw must be positive")
		return
	}
	if req.PeriodFrom == "" || req.PeriodTo == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "period_from and period_to are required")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	disbursement := &models.Disbursement{
		DEID:           deID,
		DisbursementID: uuid.New().String(),
		AmountZMW:      req.AmountZMW,
		PeriodFrom:     req.PeriodFrom,
		PeriodTo:       req.PeriodTo,
		DisbursedAt:    now,
	}

	if err := h.disbursementRepo.Create(r.Context(), disbursement); err != nil {
		h.logger.WithError(err).Error("failed to record disbursement")
		h.respondWithError(w, http.StatusInternalServerError, "DISBURSEMENT_FAILED", "Failed to record disbursement")
		return
	}

	// Advance the DE's outstanding-balance watermark. Requires the DE phone
	// (the path param is the DEID UUID, which the DE table is not keyed by).
	if strings.TrimSpace(req.DEPhone) != "" {
		if err := h.deRepo.UpdateLastDisbursedAt(r.Context(), req.DEPhone, now); err != nil {
			h.logger.WithError(err).WithField("de_phone", req.DEPhone).
				Warn("disbursement recorded but failed to update last_disbursed_at")
		}
	}

	h.respondWithJSON(w, http.StatusCreated, map[string]interface{}{
		"disbursement_id": disbursement.DisbursementID,
		"amount_zmw":      disbursement.AmountZMW,
		"disbursed_at":    disbursement.DisbursedAt,
	})
}

func (h *DisbursementHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func (h *DisbursementHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{
		Error: ErrorDetail{Code: code, Message: message},
	})
}
