package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/service"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

var (
	errInKindInvalidSKU       = errors.New("sku must be one of: mealie_bag, household_item")
	errInKindInvalidQuantity  = errors.New("quantity must be >= 1")
	errInKindOverDisbursement = errors.New("quantity exceeds outstanding count for this SKU")
	errInKindDENotFound       = errors.New("delivery executive not found")
)

type inkindDisbCreatorLister interface {
	Create(ctx context.Context, d *models.InKindDisbursement) error
	ListByDE(ctx context.Context, deID string) ([]*models.InKindDisbursement, error)
}

type inkindEarningQuerier interface {
	QueryByDE(ctx context.Context, deID, afterTimestamp string, pageSize int32, lastKey map[string]types.AttributeValue) ([]*models.EarningsLedger, map[string]types.AttributeValue, error)
}

type inkindDEReader interface {
	GetByPhone(ctx context.Context, phone string) (*models.DeliveryExecutive, error)
}

// inkindNotifier matches service.NotificationService's Send method signature.
type inkindNotifier interface {
	Send(ctx context.Context, req models.NotificationSendRequest) models.NotificationSendResult
}

type InKindDisbursementHandlers struct {
	inKindDisbRepo     inkindDisbCreatorLister
	earningsLedgerRepo inkindEarningQuerier
	deRepo             inkindDEReader
	notifier           inkindNotifier
	logger             *logrus.Logger
}

func NewInKindDisbursementHandlers(
	inKindDisbRepo *repository.InKindDisbursementRepository,
	earningsLedgerRepo *repository.EarningsLedgerRepository,
	deRepo *repository.DERepository,
	notifier service.NotificationService,
	logger *logrus.Logger,
) *InKindDisbursementHandlers {
	return &InKindDisbursementHandlers{
		inKindDisbRepo:     inKindDisbRepo,
		earningsLedgerRepo: earningsLedgerRepo,
		deRepo:             deRepo,
		notifier:           notifier,
		logger:             logger,
	}
}

type inKindDisbursementRequest struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
	Notes    string `json:"notes"`
}

// RecordInKindDisbursement handles POST /api/v1/admin/drivers/{phone}/inkind-disbursements
func (h *InKindDisbursementHandlers) RecordInKindDisbursement(w http.ResponseWriter, r *http.Request) {
	phone := normalizePhone(mux.Vars(r)["phone"])
	if phone == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "phone is required")
		return
	}

	adminID := entityIDFrom(r)

	var req inKindDisbursementRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	sku := models.InKindSKU(strings.TrimSpace(req.SKU))
	if !models.ValidInKindSKU(sku) {
		respondWithError(w, http.StatusBadRequest, "INVALID_SKU", errInKindInvalidSKU.Error())
		return
	}
	if req.Quantity < 1 {
		respondWithError(w, http.StatusBadRequest, "INVALID_QUANTITY", errInKindInvalidQuantity.Error())
		return
	}

	de, err := h.deRepo.GetByPhone(r.Context(), phone)
	if err != nil {
		h.logger.WithError(err).Error("inkind: failed to fetch DE")
		respondWithError(w, http.StatusInternalServerError, "DE_FETCH_FAILED", "Failed to fetch driver")
		return
	}
	if de == nil {
		respondWithError(w, http.StatusNotFound, "DE_NOT_FOUND", errInKindDENotFound.Error())
		return
	}

	outstanding, err := h.computeOutstanding(r.Context(), de.DEID, sku)
	if err != nil {
		h.logger.WithError(err).Error("inkind: failed to compute outstanding")
		respondWithError(w, http.StatusInternalServerError, "OUTSTANDING_FAILED", "Failed to compute outstanding count")
		return
	}
	if req.Quantity > outstanding {
		respondWithError(w, http.StatusBadRequest, "OVER_DISBURSEMENT", errInKindOverDisbursement.Error())
		return
	}

	disbursedAt := timezone.Now().Format(time.RFC3339)
	record := &models.InKindDisbursement{
		DEID:        de.DEID,
		SKU:         sku,
		Quantity:    req.Quantity,
		Notes:       strings.TrimSpace(req.Notes),
		DisbursedBy: adminID,
		DisbursedAt: disbursedAt,
	}

	if err := h.inKindDisbRepo.Create(r.Context(), record); err != nil {
		h.logger.WithError(err).Error("inkind: failed to create record")
		respondWithError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to record disbursement")
		return
	}

	go h.notifyDriver(de.DEID, sku, req.Quantity)

	respondWithJSON(w, http.StatusCreated, map[string]interface{}{
		"disbursement_id": record.DisbursementID,
		"sku":             string(record.SKU),
		"quantity":        record.Quantity,
		"disbursed_at":    record.DisbursedAt,
	})
}

// ListInKindDisbursements handles GET /api/v1/admin/drivers/{phone}/inkind-disbursements
func (h *InKindDisbursementHandlers) ListInKindDisbursements(w http.ResponseWriter, r *http.Request) {
	phone := normalizePhone(mux.Vars(r)["phone"])
	if phone == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "phone is required")
		return
	}

	de, err := h.deRepo.GetByPhone(r.Context(), phone)
	if err != nil {
		h.logger.WithError(err).Error("inkind: failed to fetch DE")
		respondWithError(w, http.StatusInternalServerError, "DE_FETCH_FAILED", "Failed to fetch driver")
		return
	}
	if de == nil {
		respondWithError(w, http.StatusNotFound, "DE_NOT_FOUND", "Driver not found")
		return
	}

	records, err := h.inKindDisbRepo.ListByDE(r.Context(), de.DEID)
	if err != nil {
		h.logger.WithError(err).Error("inkind: failed to list disbursements")
		respondWithError(w, http.StatusInternalServerError, "LIST_FAILED", "Failed to list disbursements")
		return
	}

	type item struct {
		DisbursementID string `json:"disbursement_id"`
		SKU            string `json:"sku"`
		Quantity       int    `json:"quantity"`
		Notes          string `json:"notes,omitempty"`
		DisbursedBy    string `json:"disbursed_by"`
		DisbursedAt    string `json:"disbursed_at"`
	}
	items := make([]item, 0, len(records))
	for _, rec := range records {
		items = append(items, item{
			DisbursementID: rec.DisbursementID,
			SKU:            string(rec.SKU),
			Quantity:       rec.Quantity,
			Notes:          rec.Notes,
			DisbursedBy:    rec.DisbursedBy,
			DisbursedAt:    rec.DisbursedAt,
		})
	}

	respondWithJSON(w, http.StatusOK, map[string]interface{}{"disbursements": items})
}

// computeOutstanding returns how many undisbursed items of a given SKU this DE has earned.
func (h *InKindDisbursementHandlers) computeOutstanding(ctx context.Context, deID string, sku models.InKindSKU) (int, error) {
	earningType := earningTypeForSKU(sku)

	// Count all-time earned entries for this SKU's earning type
	var earned int
	var lastKey map[string]types.AttributeValue
	for {
		entries, nextKey, err := h.earningsLedgerRepo.QueryByDE(ctx, deID, "", 50, lastKey)
		if err != nil {
			return 0, err
		}
		for _, e := range entries {
			if e.Type == earningType {
				earned++
			}
		}
		if nextKey == nil {
			break
		}
		lastKey = nextKey
	}

	// Sum all already-disbursed quantity for this SKU
	allDisbs, err := h.inKindDisbRepo.ListByDE(ctx, deID)
	if err != nil {
		return 0, err
	}
	var disbursed int
	for _, d := range allDisbs {
		if d.SKU == sku {
			disbursed += d.Quantity
		}
	}

	return earned - disbursed, nil
}

func earningTypeForSKU(sku models.InKindSKU) models.EarningType {
	switch sku {
	case models.InKindSKUMealieBag:
		return models.EarningTypeMealieBag
	case models.InKindSKUHouseholdItem:
		return models.EarningTypeHouseholdItem
	}
	return ""
}

func (h *InKindDisbursementHandlers) notifyDriver(deID string, sku models.InKindSKU, quantity int) {
	label := "Mealie Bag"
	if sku == models.InKindSKUHouseholdItem {
		label = "Household Item"
	}
	noun := label
	if quantity > 1 {
		noun = label + "s"
	}
	var body string
	if quantity == 1 {
		body = fmt.Sprintf("1 %s has been recorded as disbursed.", noun)
	} else {
		body = fmt.Sprintf("%d %s have been recorded as disbursed.", quantity, noun)
	}
	h.notifier.Send(context.Background(), models.NotificationSendRequest{
		RecipientType: models.RecipientTypeDriver,
		RecipientID:   deID,
		EventType:     "INKIND_DISBURSEMENT",
		Priority:      models.PriorityNormal,
		Title:         "Reward disbursed",
		Body:          body,
		Data: map[string]string{
			"type": "INKIND_DISBURSEMENT",
			"sku":  string(sku),
		},
	})
}
