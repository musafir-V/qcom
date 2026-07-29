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

// maxWebhookBodyBytes caps how much of an unauthenticated webhook body is read
// into memory and logged.
const maxWebhookBodyBytes = 64 << 10

func (h *WebhookHandlers) logWebhookRequest(webhook string, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBodyBytes))
	if err != nil {
		h.logger.WithError(err).WithField("webhook", webhook).Error("failed to read webhook request body")
		return
	}

	// The body carries message content (and OTP text on delivery receipts), so
	// it is logged at debug level only.
	h.logger.WithFields(logrus.Fields{
		"webhook":      webhook,
		"method":       r.Method,
		"path":         r.URL.Path,
		"content_type": r.Header.Get("Content-Type"),
		"body_bytes":   len(body),
	}).Info("whatsapp webhook received")
	h.logger.WithField("webhook", webhook).WithField("body", string(body)).Debug("whatsapp webhook body")
}

func (h *WebhookHandlers) respondOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}
