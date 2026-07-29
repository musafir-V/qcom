package handlers

import (
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
	entityID, ok := requireEntityID(w, r, "missing identity")
	if !ok {
		return
	}
	entityType := entityTypeFrom(r)

	recipientType, err := service.RecipientTypeFromJWT(entityType)
	if err != nil {
		respondWithError(w, http.StatusForbidden, "FORBIDDEN", "unsupported token type for device registration")
		return
	}

	var req upsertDeviceTokenRequest
	if !decodeJSONBody(w, r, &req) {
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
		respondWithError(w, http.StatusInternalServerError, "DEVICE_TOKEN_UPDATE_FAILED", "Failed to update device token")
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// SendNotification handles POST /internal/v1/notifications/send (private network only).
func (h *NotificationHandlers) SendNotification(w http.ResponseWriter, r *http.Request) {
	var req models.NotificationSendRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	result := h.notificationService.Send(r.Context(), req)
	status := http.StatusAccepted
	respondWithJSON(w, status, result)
}
