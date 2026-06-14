package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

var (
	errDisbursementMissingDEPhone = errors.New("de_phone is required")
	errDisbursementInvalidAmount  = errors.New("amount_zmw must be positive")
	errDisbursementMissingPeriod  = errors.New("period_from and period_to are required")
	errDisbursementInvalidPeriod  = errors.New("period_from must be on or before period_to")
	errDisbursementDEIDMismatch   = errors.New("deId does not match de_phone")
	errDisbursementDENotFound     = errors.New("delivery executive not found")
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

type disbursementRequest struct {
	AmountZMW  float64 `json:"amount_zmw"`
	PeriodFrom string  `json:"period_from"`
	PeriodTo   string  `json:"period_to"`
	DEPhone    string  `json:"de_phone"`
}

// validateDisbursementRequest checks required fields and period ordering.
// period_from/period_to are YYYY-MM-DD labels for ops records only — they do
// not drive the earnings watermark (last_disbursed_at always equals disbursed_at).
func validateDisbursementRequest(req disbursementRequest) error {
	if req.AmountZMW <= 0 {
		return errDisbursementInvalidAmount
	}
	if strings.TrimSpace(req.PeriodFrom) == "" || strings.TrimSpace(req.PeriodTo) == "" {
		return errDisbursementMissingPeriod
	}
	if req.PeriodFrom > req.PeriodTo {
		return errDisbursementInvalidPeriod
	}
	if strings.TrimSpace(req.DEPhone) == "" {
		return errDisbursementMissingDEPhone
	}
	return nil
}

func validateDEIdentity(pathDEID string, de *models.DeliveryExecutive) error {
	if de == nil {
		return errDisbursementDENotFound
	}
	if de.DEID != pathDEID {
		return errDisbursementDEIDMismatch
	}
	return nil
}

func classifyDisbursementError(err error) (int, string) {
	switch {
	case errors.Is(err, errDisbursementInvalidAmount):
		return http.StatusBadRequest, "INVALID_AMOUNT"
	case errors.Is(err, errDisbursementMissingPeriod):
		return http.StatusBadRequest, "MISSING_FIELD"
	case errors.Is(err, errDisbursementInvalidPeriod):
		return http.StatusBadRequest, "INVALID_PERIOD"
	case errors.Is(err, errDisbursementMissingDEPhone):
		return http.StatusBadRequest, "MISSING_FIELD"
	case errors.Is(err, errDisbursementDEIDMismatch):
		return http.StatusBadRequest, "DE_ID_MISMATCH"
	case errors.Is(err, errDisbursementDENotFound):
		return http.StatusNotFound, "DE_NOT_FOUND"
	default:
		return http.StatusInternalServerError, "DISBURSEMENT_FAILED"
	}
}

// POST /api/v1/de/{deId}/disbursement
// Internal ops endpoint (no auth). Records an offline payout to a DE and advances
// the DE's last_disbursed_at watermark to disbursed_at (the payout timestamp).
// Body: { "amount_zmw": 500.0, "period_from": "2026-05-01", "period_to": "2026-05-31", "de_phone": "+26097..." }
func (h *DisbursementHandlers) RecordDisbursement(w http.ResponseWriter, r *http.Request) {
	deID := mux.Vars(r)["deId"]
	if strings.TrimSpace(deID) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "deId is required")
		return
	}

	var req disbursementRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	req.DEPhone = strings.TrimSpace(req.DEPhone)
	if req.DEPhone != "" && !strings.HasPrefix(req.DEPhone, "+") {
		req.DEPhone = "+" + req.DEPhone
	}
	if req.DEPhone != "" && !isValidPhoneNumber(req.DEPhone) {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_PHONE", "Invalid phone number format")
		return
	}

	if err := validateDisbursementRequest(req); err != nil {
		status, code := classifyDisbursementError(err)
		h.respondWithError(w, status, code, err.Error())
		return
	}

	de, err := h.deRepo.GetByPhone(r.Context(), req.DEPhone)
	if err != nil {
		h.logger.WithError(err).Error("failed to fetch DE for disbursement")
		h.respondWithError(w, http.StatusInternalServerError, "DISBURSEMENT_FAILED", "Failed to fetch delivery executive")
		return
	}
	if err := validateDEIdentity(deID, de); err != nil {
		status, code := classifyDisbursementError(err)
		h.respondWithError(w, status, code, err.Error())
		return
	}

	// Watermark is always the payout instant — never derived from period_to.
	// Use Zambia RFC3339 to match earnings ledger created_at (see EarningsLedger.Append).
	disbursedAt := timezone.Now().Format(time.RFC3339)
	disbursement := &models.Disbursement{
		DEID:           deID,
		DisbursementID: uuid.New().String(),
		AmountZMW:      req.AmountZMW,
		PeriodFrom:     strings.TrimSpace(req.PeriodFrom),
		PeriodTo:       strings.TrimSpace(req.PeriodTo),
		DisbursedAt:    disbursedAt,
	}

	if err := h.disbursementRepo.Create(r.Context(), disbursement); err != nil {
		h.logger.WithError(err).Error("failed to record disbursement")
		h.respondWithError(w, http.StatusInternalServerError, "DISBURSEMENT_FAILED", "Failed to record disbursement")
		return
	}

	if err := h.deRepo.UpdateLastDisbursedAt(r.Context(), req.DEPhone, disbursedAt); err != nil {
		h.logger.WithError(err).WithFields(logrus.Fields{
			"de_phone": req.DEPhone, "de_id": deID, "disbursed_at": disbursedAt,
		}).Error("disbursement recorded but failed to update last_disbursed_at")
		h.respondWithError(w, http.StatusInternalServerError, "WATERMARK_UPDATE_FAILED",
			fmt.Sprintf("Disbursement recorded (%s) but failed to update earnings watermark — fix last_disbursed_at manually",
				disbursement.DisbursementID))
		return
	}

	h.respondWithJSON(w, http.StatusCreated, map[string]interface{}{
		"disbursement_id": disbursement.DisbursementID,
		"amount_zmw":      disbursement.AmountZMW,
		"disbursed_at":    disbursement.DisbursedAt,
		"period_from":     disbursement.PeriodFrom,
		"period_to":       disbursement.PeriodTo,
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
