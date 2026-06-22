package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

type EarningsHandlers struct {
	earningsLedgerRepo earningsSummaryLedgerQuerier
	disbursementRepo   earningsDisbursementLister
	deRepo             earningsDEReader
	logger             *logrus.Logger
}

type earningsSummaryLedgerQuerier interface {
	QueryByDE(ctx context.Context, deID, afterTimestamp string, pageSize int32, lastKey map[string]types.AttributeValue) ([]*models.EarningsLedger, map[string]types.AttributeValue, error)
}

type earningsDisbursementLister interface {
	ListByDE(ctx context.Context, deID string) ([]*models.Disbursement, error)
}

type earningsDEReader interface {
	GetByPhone(ctx context.Context, phone string) (*models.DeliveryExecutive, error)
}

func NewEarningsHandlers(
	earningsLedgerRepo *repository.EarningsLedgerRepository,
	disbursementRepo *repository.DisbursementRepository,
	deRepo *repository.DERepository,
	logger *logrus.Logger,
) *EarningsHandlers {
	return &EarningsHandlers{
		earningsLedgerRepo: earningsLedgerRepo,
		disbursementRepo:   disbursementRepo,
		deRepo:             deRepo,
		logger:             logger,
	}
}

// GET /api/v1/de/earnings/summary
// Returns outstanding balance (sum since last disbursement), category breakdown
// (live order earnings vs bonuses), and a paginated, newest-first list of line items.
func (h *EarningsHandlers) GetEarningsSummary(w http.ResponseWriter, r *http.Request) {
	dePhone, _ := r.Context().Value("phone").(string)
	cursor := r.URL.Query().Get("cursor")

	de, err := h.deRepo.GetByPhone(r.Context(), dePhone)
	if err != nil || de == nil {
		h.logger.WithError(err).Error("failed to fetch DE for earnings summary")
		h.respondWithError(w, http.StatusInternalServerError, "DE_FETCH_FAILED", "Failed to fetch DE")
		return
	}

	deID := de.DEID
	afterTimestamp := de.LastDisbursedAt // empty string = all time

	var lastKey map[string]types.AttributeValue
	if cursor != "" {
		lastKey = map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "EARN!" + deID},
			"SK": &types.AttributeValueMemberS{Value: cursor},
		}
	}

	entries, nextKey, err := h.earningsLedgerRepo.QueryByDE(r.Context(), deID, afterTimestamp, 20, lastKey)
	if err != nil {
		h.logger.WithError(err).Error("failed to query earnings ledger")
		h.respondWithError(w, http.StatusInternalServerError, "EARNINGS_FETCH_FAILED", "Failed to fetch earnings")
		return
	}

	liveOrderTotal, bonusTotal, outstandingZMW, err := h.computeBreakdown(r.Context(), deID, afterTimestamp)
	if err != nil {
		h.logger.WithError(err).Error("failed to compute earnings breakdown")
		h.respondWithError(w, http.StatusInternalServerError, "EARNINGS_SUM_FAILED", "Failed to compute balance")
		return
	}

	type lineItem struct {
		EarningID   string  `json:"earning_id"`
		Type        string  `json:"type"`
		AmountZMW   float64 `json:"amount_zmw"`
		Label       string  `json:"label,omitempty"`
		CreatedAt   string  `json:"created_at"`
		ReferenceID string  `json:"reference_id"`
	}
	items := make([]lineItem, 0, len(entries))
	for _, e := range entries {
		items = append(items, lineItem{
			EarningID:   e.EarningID,
			Type:        string(e.Type),
			AmountZMW:   e.AmountZMW,
			Label:       e.Label,
			CreatedAt:   e.CreatedAt,
			ReferenceID: e.ReferenceID,
		})
	}

	var nextCursor *string
	if nextKey != nil {
		if sk, ok := nextKey["SK"].(*types.AttributeValueMemberS); ok {
			nextCursor = &sk.Value
		}
	}

	h.respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"outstanding_balance_zmw": outstandingZMW,
		"live_order_total_zmw":    liveOrderTotal,
		"bonus_total_zmw":         bonusTotal,
		"line_items":              items,
		"next_cursor":             nextCursor,
	})
}

// GET /api/v1/de/earnings/disbursements
// Returns all disbursements for the calling DE, newest first.
func (h *EarningsHandlers) GetDisbursements(w http.ResponseWriter, r *http.Request) {
	dePhone, _ := r.Context().Value("phone").(string)

	de, err := h.deRepo.GetByPhone(r.Context(), dePhone)
	if err != nil || de == nil {
		h.logger.WithError(err).Error("failed to fetch DE for disbursements")
		h.respondWithError(w, http.StatusInternalServerError, "DE_FETCH_FAILED", "Failed to fetch DE")
		return
	}

	disbursements, err := h.disbursementRepo.ListByDE(r.Context(), de.DEID)
	if err != nil {
		h.logger.WithError(err).Error("failed to list disbursements")
		h.respondWithError(w, http.StatusInternalServerError, "DISBURSEMENT_FETCH_FAILED", "Failed to fetch disbursements")
		return
	}

	type item struct {
		DisbursementID string  `json:"disbursement_id"`
		AmountZMW      float64 `json:"amount_zmw"`
		PeriodFrom     string  `json:"period_from"`
		PeriodTo       string  `json:"period_to"`
		DisbursedAt    string  `json:"disbursed_at"`
	}
	items := make([]item, 0, len(disbursements))
	for _, d := range disbursements {
		items = append(items, item{
			DisbursementID: d.DisbursementID,
			AmountZMW:      d.AmountZMW,
			PeriodFrom:     d.PeriodFrom,
			PeriodTo:       d.PeriodTo,
			DisbursedAt:    d.DisbursedAt,
		})
	}

	h.respondWithJSON(w, http.StatusOK, map[string]interface{}{"disbursements": items})
}

// computeBreakdown sums ledger entries by category (trip vs bonus) across all
// pages since the last disbursement.
func (h *EarningsHandlers) computeBreakdown(ctx context.Context, deID, afterTimestamp string) (float64, float64, float64, error) {
	var liveTotal, bonusTotal, outstandingTotal float64
	var lastKey map[string]types.AttributeValue
	for {
		entries, nextKey, err := h.earningsLedgerRepo.QueryByDE(ctx, deID, afterTimestamp, 50, lastKey)
		if err != nil {
			return 0, 0, 0, err
		}
		for _, e := range entries {
			if !models.IsPositiveCashEarning(e) {
				continue
			}
			outstandingTotal += e.AmountZMW
			if e.Type == models.EarningTypeTrip {
				liveTotal += e.AmountZMW
			} else {
				bonusTotal += e.AmountZMW
			}
		}
		if nextKey == nil {
			break
		}
		lastKey = nextKey
	}
	return liveTotal, bonusTotal, outstandingTotal, nil
}

func (h *EarningsHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func (h *EarningsHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{
		Error: ErrorDetail{Code: code, Message: message},
	})
}
