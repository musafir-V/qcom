package handlers

import (
	"io"
	"net/http"

	"github.com/sirupsen/logrus"
)

type WebhookHandlers struct {
	logger *logrus.Logger
}

func NewWebhookHandlers(logger *logrus.Logger) *WebhookHandlers {
	return &WebhookHandlers{logger: logger}
}

// POST /webhooks/outbound-whatsapp-message-status
func (h *WebhookHandlers) OutboundWhatsAppMessageStatus(w http.ResponseWriter, r *http.Request) {
	h.logWebhookRequest("outbound-whatsapp-message-status", r)
	h.respondOK(w)
}

// POST /webhooks/inbound-whatsapp-message
func (h *WebhookHandlers) InboundWhatsAppMessage(w http.ResponseWriter, r *http.Request) {
	h.logWebhookRequest("inbound-whatsapp-message", r)
	h.respondOK(w)
}

func (h *WebhookHandlers) logWebhookRequest(webhook string, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.logger.WithError(err).WithField("webhook", webhook).Error("failed to read webhook request body")
		return
	}

	h.logger.WithFields(logrus.Fields{
		"webhook":      webhook,
		"method":       r.Method,
		"path":         r.URL.Path,
		"content_type": r.Header.Get("Content-Type"),
		"body":         string(body),
	}).Info("whatsapp webhook received")
}

func (h *WebhookHandlers) respondOK(w http.ResponseWriter) {
	writeJSON(w, h.logger, http.StatusOK, map[string]string{"status": "ok"})
}
