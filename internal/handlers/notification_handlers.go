package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type NotificationHandlers struct {
	notificationService service.NotificationService
	logger              *logrus.Logger
}

func NewNotificationHandlers(notificationService service.NotificationService, logger *logrus.Logger) *NotificationHandlers {
	return &NotificationHandlers{
		notificationService: notificationService,
		logger:              logger,
	}
}

type upsertDeviceTokenRequest struct {
	FCMToken string `json:"fcm_token"`
	Platform string `json:"platform"`
}

// PutDeviceToken handles PUT /api/v1/device-token for customer and driver JWTs.
func (h *NotificationHandlers) PutDeviceToken(w http.ResponseWriter, r *http.Request) {
	entityID, _ := r.Context().Value("entity_id").(string)
	entityType, _ := r.Context().Value("entity_type").(string)
	if entityID == "" {
		h.respondWithError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing identity")
		return
	}

	recipientType, err := service.RecipientTypeFromJWT(entityType)
	if err != nil {
		h.respondWithError(w, http.StatusForbidden, "FORBIDDEN", "unsupported token type for device registration")
		return
	}

	var req upsertDeviceTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	if err := h.notificationService.UpsertDeviceToken(
		r.Context(),
		recipientType,
		entityID,
		strings.TrimSpace(req.FCMToken),
		strings.TrimSpace(req.Platform),
	); err != nil {
		h.logger.WithError(err).Error("failed to upsert device token")
		h.respondWithError(w, http.StatusInternalServerError, "DEVICE_TOKEN_UPDATE_FAILED", "Failed to update device token")
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// SendNotification handles POST /internal/v1/notifications/send (private network only).
func (h *NotificationHandlers) SendNotification(w http.ResponseWriter, r *http.Request) {
	var req models.NotificationSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	result := h.notificationService.Send(r.Context(), req)
	status := http.StatusAccepted
	h.respondWithJSON(w, status, result)
}

func (h *NotificationHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func (h *NotificationHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{
		Error: ErrorDetail{Code: code, Message: message},
	})
}
