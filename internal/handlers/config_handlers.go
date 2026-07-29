package handlers

import (
	"net/http"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

type ConfigHandlers struct {
	payoutConfigRepo *repository.PayoutConfigRepository
	logger           *logrus.Logger
}

func NewConfigHandlers(payoutConfigRepo *repository.PayoutConfigRepository, logger *logrus.Logger) *ConfigHandlers {
	return &ConfigHandlers{payoutConfigRepo: payoutConfigRepo, logger: logger}
}

// PATCH /api/v1/config/payout
// Body: { "field": "referral_bonus_zmw", "value": "25.5" }
// Updates a single named field in the payout config. No auth required.
// Numeric fields are stored as DynamoDB Number type. String fields as String.
func (h *ConfigHandlers) UpdatePayoutConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Field string `json:"field"`
		Value string `json:"value"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Field == "" || req.Value == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "field and value are required")
		return
	}

	// Attempt to parse as number first; fall back to string
	var attrValue types.AttributeValue
	if _, err := strconv.ParseFloat(req.Value, 64); err == nil {
		attrValue = &types.AttributeValueMemberN{Value: req.Value}
	} else {
		attrValue = &types.AttributeValueMemberS{Value: req.Value}
	}

	if err := h.payoutConfigRepo.UpdateField(r.Context(), req.Field, attrValue); err != nil {
		h.logger.WithError(err).Error("failed to update payout config")
		respondWithError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update config")
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]string{
		"field":  req.Field,
		"value":  req.Value,
		"status": "updated",
	})
}
