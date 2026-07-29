package handlers

import (
	"context"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

type earningsInKindDisbursementLister interface {
	ListByDE(ctx context.Context, deID string) ([]*models.InKindDisbursement, error)
}

type EarningsHandlers struct {
	earningsLedgerRepo earningsSummaryLedgerQuerier
	disbursementRepo   earningsDisbursementLister
	inKindDisbRepo     earningsInKindDisbursementLister
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
	inKindDisbRepo *repository.InKindDisbursementRepository,
	deRepo *repository.DERepository,
	logger *logrus.Logger,
) *EarningsHandlers {
	return &EarningsHandlers{
		earningsLedgerRepo: earningsLedgerRepo,
		disbursementRepo:   disbursementRepo,
		inKindDisbRepo:     inKindDisbRepo,
		deRepo:             deRepo,
		logger:             logger,
	}
}

// GET /api/v1/de/earnings/summary
// Returns outstanding balance (sum since last disbursement), category breakdown
// (live order earnings vs bonuses), and a paginated, newest-first list of line items.
func (h *EarningsHandlers) GetEarningsSummary(w http.ResponseWriter, r *http.Request) {
	dePhone := phoneFrom(r)
	cursor := r.URL.Query().Get("cursor")

	de, err := h.deRepo.GetByPhone(r.Context(), dePhone)
	if err != nil || de == nil {
		h.logger.WithError(err).Error("failed to fetch DE for earnings summary")
		respondWithError(w, http.StatusInternalServerError, "DE_FETCH_FAILED", "Failed to fetch DE")
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
		respondWithError(w, http.StatusInternalServerError, "EARNINGS_FETCH_FAILED", "Failed to fetch earnings")
		return
	}

	liveOrderTotal, bonusTotal, outstandingZMW, err := h.computeBreakdown(r.Context(), deID, afterTimestamp)
	if err != nil {
		h.logger.WithError(err).Error("failed to compute earnings breakdown")
		respondWithError(w, http.StatusInternalServerError, "EARNINGS_SUM_FAILED", "Failed to compute balance")
		return
	}

	inKindItems, err := h.computeInKindSummary(r.Context(), deID)
	if err != nil {
		h.logger.WithError(err).Error("failed to compute in-kind summary")
		respondWithError(w, http.StatusInternalServerError, "IN_KIND_SUMMARY_FAILED", "Failed to compute in-kind summary")
		return
	}

	type lineItem struct {
		EarningID   string  `json:"earning_id"`
		Type        string  `json:"type"`
		AmountZMW   float64 `json:"amount_zmw"`
		Label       string  `json:"label,omitempty"`
		CreatedAt   string  `json:"created_at"`
		ReferenceID string  `json:"reference_id"`
		DistanceKM  float64 `json:"distance_km,omitempty"`
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
			DistanceKM:  e.DistanceKM,
		})
	}

	var nextCursor *string
	if nextKey != nil {
		if sk, ok := nextKey["SK"].(*types.AttributeValueMemberS); ok {
			nextCursor = &sk.Value
		}
	}

	respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"outstanding_balance_zmw": outstandingZMW,
		"live_order_total_zmw":    liveOrderTotal,
		"bonus_total_zmw":         bonusTotal,
		"line_items":              items,
		"next_cursor":             nextCursor,
		"in_kind_summary":         inKindItems,
	})
}

// GET /api/v1/de/earnings/disbursements
// Returns all disbursements for the calling DE, newest first.
func (h *EarningsHandlers) GetDisbursements(w http.ResponseWriter, r *http.Request) {
	dePhone := phoneFrom(r)

	de, err := h.deRepo.GetByPhone(r.Context(), dePhone)
	if err != nil || de == nil {
		h.logger.WithError(err).Error("failed to fetch DE for disbursements")
		respondWithError(w, http.StatusInternalServerError, "DE_FETCH_FAILED", "Failed to fetch DE")
		return
	}

	disbursements, err := h.disbursementRepo.ListByDE(r.Context(), de.DEID)
	if err != nil {
		h.logger.WithError(err).Error("failed to list disbursements")
		respondWithError(w, http.StatusInternalServerError, "DISBURSEMENT_FETCH_FAILED", "Failed to fetch disbursements")
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

	respondWithJSON(w, http.StatusOK, map[string]interface{}{"disbursements": items})
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

type inKindSummaryItem struct {
	SKU         string `json:"sku"`
	Label       string `json:"label"`
	Earned      int    `json:"earned"`
	Disbursed   int    `json:"disbursed"`
	Outstanding int    `json:"outstanding"`
}

// computeInKindSummary returns a per-SKU breakdown of earned vs disbursed in-kind rewards.
// Only SKUs with earned > 0 are included. Never returns nil — always an empty slice at minimum.
func (h *EarningsHandlers) computeInKindSummary(ctx context.Context, deID string) ([]inKindSummaryItem, error) {
	// Count all-time earned entries per earning type (afterTimestamp="" = all time)
	earnedCounts := map[models.EarningType]int{}
	var lastKey map[string]types.AttributeValue
	for {
		entries, nextKey, err := h.earningsLedgerRepo.QueryByDE(ctx, deID, "", 50, lastKey)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			switch e.Type {
			case models.EarningTypeMealieBag, models.EarningTypeHouseholdItem:
				earnedCounts[e.Type]++
			}
		}
		if nextKey == nil {
			break
		}
		lastKey = nextKey
	}

	// Sum quantity_disbursed per SKU from all in-kind disbursements
	disbursedQty := map[models.InKindSKU]int{}
	inKindDisbs, err := h.inKindDisbRepo.ListByDE(ctx, deID)
	if err != nil {
		return nil, err
	}
	for _, d := range inKindDisbs {
		disbursedQty[d.SKU] += d.Quantity
	}

	// Build result — only include SKUs with at least one earned entry
	skuDefs := []struct {
		earningType models.EarningType
		sku         models.InKindSKU
		label       string
	}{
		{models.EarningTypeMealieBag, models.InKindSKUMealieBag, "Mealie Bag"},
		{models.EarningTypeHouseholdItem, models.InKindSKUHouseholdItem, "Household Item"},
	}

	result := make([]inKindSummaryItem, 0)
	for _, s := range skuDefs {
		earned := earnedCounts[s.earningType]
		if earned == 0 {
			continue
		}
		d := disbursedQty[s.sku]
		result = append(result, inKindSummaryItem{
			SKU:         string(s.sku),
			Label:       s.label,
			Earned:      earned,
			Disbursed:   d,
			Outstanding: earned - d,
		})
	}
	return result, nil
}
