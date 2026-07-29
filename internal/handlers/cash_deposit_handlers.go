package handlers

import (
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
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "phone is required")
		return
	}

	var req cashDepositRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	result, err := h.depositService.RecordDeposit(r.Context(), phone, req.DepositID, req.AmountZMW)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidDepositAmount):
			respondWithError(w, http.StatusBadRequest, "INVALID_AMOUNT", err.Error())
		case errors.Is(err, service.ErrNoCashInHand):
			respondWithError(w, http.StatusBadRequest, "NO_CASH_IN_HAND", err.Error())
		case errors.Is(err, service.ErrDepositDENotFound):
			respondWithError(w, http.StatusNotFound, "DE_NOT_FOUND", err.Error())
		case errors.Is(err, service.ErrDepositConflict):
			// Optimistic-lock clash or idempotent replay — expected, not an alert.
			respondWithError(w, http.StatusConflict, "DEPOSIT_FAILED", err.Error())
		default:
			h.logger.WithError(err).Error("unexpected error recording cash deposit")
			respondWithError(w, http.StatusInternalServerError, "DEPOSIT_ERROR", "internal error recording cash deposit")
		}
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"in_hand_cash_zmw": result.NewBalanceZMW,
		"cash_blocked":     result.CashBlocked,
	})
}
