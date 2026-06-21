package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
	"google.golang.org/api/option"
)

var (
	ErrInvalidNotificationRequest = errors.New("invalid notification request")
	ErrUnsupportedRecipientType   = errors.New("unsupported recipient type")
)

// driverChannelID must match driver-app orderChannel.ts (importance only; no custom sound).
const driverChannelID = "order-alert-v3"

var eventMinPriority = map[string]models.NotificationPriority{
	"ORDER_ASSIGNED": models.PriorityCritical,
}

// NotificationService is the central FCM sender and token store owner.
type NotificationService interface {
	Send(ctx context.Context, req models.NotificationSendRequest) models.NotificationSendResult
	UpsertDeviceToken(ctx context.Context, recipientType models.RecipientType, recipientID, token, platform string) error
	ClearDeviceToken(ctx context.Context, recipientType models.RecipientType, recipientID string) error
}

// RecipientTypeFromJWT maps JWT entity_type to notification recipient_type.
func RecipientTypeFromJWT(entityType string) (models.RecipientType, error) {
	switch entityType {
	case "customer":
		return models.RecipientTypeCustomer, nil
	case "de":
		return models.RecipientTypeDriver, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedRecipientType, entityType)
	}
}

type fcmNotificationService struct {
	client    *messaging.Client
	tokenRepo *repository.DeviceTokenRepository
	logger    *logrus.Logger
}

type noopNotificationService struct {
	logger *logrus.Logger
}

func (n *noopNotificationService) Send(_ context.Context, req models.NotificationSendRequest) models.NotificationSendResult {
	n.logger.WithFields(logrus.Fields{
		"recipient_type": req.RecipientType,
		"recipient_id":   req.RecipientID,
		"event_type":     req.EventType,
	}).Debug("notification service disabled — skipping push")
	return models.NotificationSendResult{Status: models.SendStatusSkipped, Reason: "push_disabled"}
}

func (n *noopNotificationService) UpsertDeviceToken(_ context.Context, _ models.RecipientType, _, _, _ string) error {
	n.logger.Debug("notification service disabled — skipping token upsert")
	return nil
}

func (n *noopNotificationService) ClearDeviceToken(_ context.Context, _ models.RecipientType, _ string) error {
	n.logger.Debug("notification service disabled — skipping token clear")
	return nil
}

// NewNotificationService builds the live FCM service when credentials are present
// and valid; otherwise it logs and returns a no-op so the server still boots.
func NewNotificationService(cfg *config.FirebaseConfig, tokenRepo *repository.DeviceTokenRepository, logger *logrus.Logger) NotificationService {
	if cfg.CredentialsB64 == "" {
		logger.Warn("notification service: FIREBASE_CREDENTIALS_B64 not set — push disabled (no-op)")
		return &noopNotificationService{logger: logger}
	}
	credsJSON, err := base64.StdEncoding.DecodeString(cfg.CredentialsB64)
	if err != nil {
		logger.WithError(err).Error("notification service: FIREBASE_CREDENTIALS_B64 is not valid base64 — push disabled (no-op)")
		return &noopNotificationService{logger: logger}
	}
	app, err := firebase.NewApp(context.Background(), nil, option.WithCredentialsJSON(credsJSON))
	if err != nil {
		logger.WithError(err).Error("notification service: failed to init firebase app — push disabled (no-op)")
		return &noopNotificationService{logger: logger}
	}
	client, err := app.Messaging(context.Background())
	if err != nil {
		logger.WithError(err).Error("notification service: failed to init messaging client — push disabled (no-op)")
		return &noopNotificationService{logger: logger}
	}
	logger.Info("notification service: FCM enabled")
	return &fcmNotificationService{client: client, tokenRepo: tokenRepo, logger: logger}
}

func (s *fcmNotificationService) UpsertDeviceToken(ctx context.Context, recipientType models.RecipientType, recipientID, token, platform string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return s.tokenRepo.Delete(ctx, recipientType, recipientID)
	}
	return s.tokenRepo.Upsert(ctx, recipientType, recipientID, token, platform)
}

func (s *fcmNotificationService) ClearDeviceToken(ctx context.Context, recipientType models.RecipientType, recipientID string) error {
	return s.tokenRepo.Delete(ctx, recipientType, recipientID)
}

func (s *fcmNotificationService) Send(ctx context.Context, req models.NotificationSendRequest) models.NotificationSendResult {
	if err := validateSendRequest(req); err != nil {
		s.logger.WithError(err).Warn("notification send rejected")
		return models.NotificationSendResult{Status: models.SendStatusSkipped, Reason: err.Error()}
	}

	record, err := s.tokenRepo.Get(ctx, req.RecipientType, req.RecipientID)
	if err != nil {
		s.logger.WithError(err).WithField("recipient_id", req.RecipientID).Warn("device token lookup failed")
		return models.NotificationSendResult{Status: models.SendStatusSkipped, Reason: "token_lookup_failed"}
	}
	if record == nil || record.FCMToken == "" {
		s.logger.WithFields(logrus.Fields{
			"recipient_type": req.RecipientType,
			"recipient_id":   req.RecipientID,
			"event_type":     req.EventType,
		}).Debug("push skipped — no device token registered")
		return models.NotificationSendResult{Status: models.SendStatusSkipped, Reason: "no_token"}
	}

	msg := buildFCMMessage(record.FCMToken, req)
	id, err := s.client.Send(ctx, msg)
	if err != nil {
		s.handleSendError(ctx, req.RecipientType, req.RecipientID, err)
		return models.NotificationSendResult{Status: models.SendStatusSkipped, Reason: "send_failed"}
	}

	s.logger.WithFields(logrus.Fields{
		"recipient_type": req.RecipientType,
		"recipient_id":   req.RecipientID,
		"event_type":     req.EventType,
		"message_id":     id,
	}).Info("push notification sent")
	return models.NotificationSendResult{Status: models.SendStatusSent, MessageID: id}
}

func validateSendRequest(req models.NotificationSendRequest) error {
	if req.RecipientID == "" || req.EventType == "" {
		return fmt.Errorf("%w: recipient_id and event_type are required", ErrInvalidNotificationRequest)
	}
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Body) == "" {
		return fmt.Errorf("%w: title and body are required", ErrInvalidNotificationRequest)
	}
	switch req.RecipientType {
	case models.RecipientTypeDriver, models.RecipientTypeCustomer, models.RecipientTypePicker:
	default:
		return fmt.Errorf("%w: invalid recipient_type %q", ErrInvalidNotificationRequest, req.RecipientType)
	}
	if !isValidPriority(req.Priority) {
		return fmt.Errorf("%w: invalid priority %q", ErrInvalidNotificationRequest, req.Priority)
	}
	if min, ok := eventMinPriority[req.EventType]; ok && priorityRank(req.Priority) < priorityRank(min) {
		return fmt.Errorf("%w: event %s requires minimum priority %s", ErrInvalidNotificationRequest, req.EventType, min)
	}
	return nil
}

func isValidPriority(p models.NotificationPriority) bool {
	switch p {
	case models.PriorityCritical, models.PriorityHigh, models.PriorityNormal:
		return true
	default:
		return false
	}
}

func priorityRank(p models.NotificationPriority) int {
	switch p {
	case models.PriorityCritical:
		return 3
	case models.PriorityHigh:
		return 2
	case models.PriorityNormal:
		return 1
	default:
		return 0
	}
}

func buildFCMMessage(token string, req models.NotificationSendRequest) *messaging.Message {
	data := map[string]string{"type": req.EventType}
	for k, v := range req.Data {
		data[k] = v
	}

	highTransport := req.Priority == models.PriorityCritical || req.Priority == models.PriorityHigh

	msg := &messaging.Message{
		Token: token,
		Data:  data,
		Notification: &messaging.Notification{
			Title: req.Title,
			Body:  req.Body,
		},
	}

	if highTransport {
		msg.Android = &messaging.AndroidConfig{Priority: "high"}
		if req.RecipientType == models.RecipientTypeDriver {
			msg.Android.Notification = &messaging.AndroidNotification{
				ChannelID: driverChannelID,
				Priority:  messaging.PriorityHigh,
				Tag:       data["trip_id"],
			}
		} else {
			msg.Android.Notification = &messaging.AndroidNotification{
				Priority: messaging.PriorityHigh,
			}
		}
		msg.APNS = &messaging.APNSConfig{
			Headers: map[string]string{"apns-priority": "10"},
			Payload: &messaging.APNSPayload{
				Aps: &messaging.Aps{
					Alert: &messaging.ApsAlert{Title: req.Title, Body: req.Body},
				},
			},
		}
		if tripID := data["trip_id"]; tripID != "" {
			msg.APNS.Headers["apns-collapse-id"] = tripID
		}
	} else {
		msg.Android = &messaging.AndroidConfig{Priority: "normal"}
	}

	return msg
}

func (s *fcmNotificationService) handleSendError(ctx context.Context, recipientType models.RecipientType, recipientID string, err error) {
	if messaging.IsUnregistered(err) || messaging.IsInvalidArgument(err) {
		s.logger.WithFields(logrus.Fields{
			"recipient_type": recipientType,
			"recipient_id":   recipientID,
		}).Info("FCM token invalid — clearing device token")
		if cerr := s.tokenRepo.Delete(ctx, recipientType, recipientID); cerr != nil {
			s.logger.WithError(cerr).Warn("failed to clear invalid device token")
		}
		return
	}
	s.logger.WithError(err).WithField("recipient_id", recipientID).Warn("push notification failed")
}
