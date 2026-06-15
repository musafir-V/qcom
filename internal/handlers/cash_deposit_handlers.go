package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type CashDepositHandlers struct {
	depositService *service.CashDepositService
	logger         *logrus.Logger
}

func NewCashDepositHandlers(depositService *service.CashDepositService, logger *logrus.Logger) *CashDepositHandlers {
	return &CashDepositHandlers{depositService: depositService, logger: logger}
}

type cashDepositRequest struct {
	AmountZMW float64 `json:"amount_zmw"`
	DepositID string  `json:"deposit_id"`
}

// POST /api/v1/admin/de/{phone}/cash-deposit
func (h *CashDepositHandlers) RecordCashDeposit(w http.ResponseWriter, r *http.Request) {
	phone := mux.Vars(r)["phone"]
	if phone == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "phone is required")
		return
	}

	var req cashDepositRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	result, err := h.depositService.RecordDeposit(r.Context(), phone, req.DepositID, req.AmountZMW)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidDepositAmount):
			h.respondWithError(w, http.StatusBadRequest, "INVALID_AMOUNT", err.Error())
		case errors.Is(err, service.ErrNoCashInHand):
			h.respondWithError(w, http.StatusBadRequest, "NO_CASH_IN_HAND", err.Error())
		case errors.Is(err, service.ErrDepositDENotFound):
			h.respondWithError(w, http.StatusNotFound, "DE_NOT_FOUND", err.Error())
		case errors.Is(err, service.ErrDepositConflict):
			// Optimistic-lock clash or idempotent replay — expected, not an alert.
			h.respondWithError(w, http.StatusConflict, "DEPOSIT_FAILED", err.Error())
		default:
			h.logger.WithError(err).Error("unexpected error recording cash deposit")
			h.respondWithError(w, http.StatusInternalServerError, "DEPOSIT_ERROR", "internal error recording cash deposit")
		}
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"in_hand_cash_zmw": result.NewBalanceZMW,
		"cash_blocked":     result.CashBlocked,
	})
}

func (h *CashDepositHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (h *CashDepositHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{
		Error: ErrorDetail{Code: code, Message: message},
	})
}
