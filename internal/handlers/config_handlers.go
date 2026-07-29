package handlers

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

// payoutConfigFields is the set of attribute names a caller may write, derived
// from the PayoutConfig dynamodbav tags so it cannot drift from the model.
var payoutConfigFields = func() map[string]struct{} {
	t := reflect.TypeOf(models.PayoutConfig{})
	fields := make(map[string]struct{}, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		if name := strings.Split(t.Field(i).Tag.Get("dynamodbav"), ",")[0]; name != "" && name != "-" {
			fields[name] = struct{}{}
		}
	}
	return fields
}()

type ConfigHandlers struct {
	payoutConfigRepo *repository.PayoutConfigRepository
	logger           *logrus.Logger
}

func NewConfigHandlers(payoutConfigRepo *repository.PayoutConfigRepository, logger *logrus.Logger) *ConfigHandlers {
	return &ConfigHandlers{payoutConfigRepo: payoutConfigRepo, logger: logger}
}

// PATCH /api/v1/config/payout
// Body: { "field": "referral_bonus_zmw", "value": "25.5" }
// Updates a single named field in the payout config. Requires an admin token.
// Numeric fields are stored as DynamoDB Number type. String fields as String.
func (h *ConfigHandlers) UpdatePayoutConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Field string `json:"field"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if req.Field == "" || req.Value == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "field and value are required")
		return
	}
	if _, ok := payoutConfigFields[req.Field]; !ok {
		h.respondWithError(w, http.StatusBadRequest, "UNKNOWN_FIELD", "field is not a payout config attribute")
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
		h.respondWithError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update config")
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]string{
		"field":  req.Field,
		"value":  req.Value,
		"status": "updated",
	})
}

func (h *ConfigHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func (h *ConfigHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{
		Error: ErrorDetail{Code: code, Message: message},
	})
}
