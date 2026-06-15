package service

import (
	"context"
	"encoding/base64"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
	"google.golang.org/api/option"
)

// assignmentChannelID must match the driver app's expo-notifications channel
// (driver-app/src/services/pollingService.ts ALERT_CHANNEL) and its custom sound.
const assignmentChannelID = "order-alert"

// assignmentSound is the bare resource name (no extension) of the channel sound.
const assignmentSound = "order_alarm"

// NotificationService delivers driver-facing push notifications. Implementations
// must be safe to call from a goroutine and must never panic on bad input.
type NotificationService interface {
	NotifyOrderAssigned(ctx context.Context, de *models.DeliveryExecutive, trip *models.Trip)
}

// buildAssignmentMessage constructs the FCM message for an order assignment.
// Data-only on Android (no Notification/Android.Notification block) so the
// driver app's FCM background handler can render a full-screen-intent
// notification via Notifee. iOS can't run JS in the background, so an APNS
// alert + custom sound is attached for it. Pure (no I/O) so it is unit-testable.
func buildAssignmentMessage(token string, trip *models.Trip) *messaging.Message {
	return &messaging.Message{
		Token: token,
		Data: map[string]string{
			"type":            "ORDER_ASSIGNED",
			"trip_id":         trip.TripID,
			"order_id":        trip.OrderID,
			"accept_deadline": trip.AcceptDeadline,
		},
		Android: &messaging.AndroidConfig{
			Priority: "high",
		},
		APNS: &messaging.APNSConfig{
			Headers: map[string]string{"apns-priority": "10"},
			Payload: &messaging.APNSPayload{
				Aps: &messaging.Aps{
					Alert: &messaging.ApsAlert{
						Title: "New order!",
						Body:  "Tap to view your trip.",
					},
					Sound: assignmentSound + ".wav",
				},
			},
		},
	}
}

type fcmNotificationService struct {
	client *messaging.Client
	deRepo *repository.DERepository
	logger *logrus.Logger
}

type noopNotificationService struct {
	logger *logrus.Logger
}

func (n *noopNotificationService) NotifyOrderAssigned(_ context.Context, de *models.DeliveryExecutive, trip *models.Trip) {
	n.logger.WithFields(logrus.Fields{"de_id": de.DEID, "trip_id": trip.TripID}).
		Debug("notification service disabled — skipping ORDER_ASSIGNED push")
}

// NewNotificationService builds the live FCM service when credentials are present
// and valid; otherwise it logs and returns a no-op so the server still boots.
func NewNotificationService(cfg *config.FirebaseConfig, deRepo *repository.DERepository, logger *logrus.Logger) NotificationService {
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
	return &fcmNotificationService{client: client, deRepo: deRepo, logger: logger}
}

func (s *fcmNotificationService) NotifyOrderAssigned(ctx context.Context, de *models.DeliveryExecutive, trip *models.Trip) {
	if de.FCMToken == "" {
		s.logger.WithField("de_id", de.DEID).Debug("ORDER_ASSIGNED push skipped — DE has no fcm_token")
		return
	}
	msg := buildAssignmentMessage(de.FCMToken, trip)
	id, err := s.client.Send(ctx, msg)
	if err != nil {
		s.handleSendError(ctx, de, err)
		return
	}
	s.logger.WithFields(logrus.Fields{"de_id": de.DEID, "trip_id": trip.TripID, "message_id": id}).
		Info("ORDER_ASSIGNED push sent")
}

// handleSendError clears the stored token when FCM says it is permanently invalid.
func (s *fcmNotificationService) handleSendError(ctx context.Context, de *models.DeliveryExecutive, err error) {
	if messaging.IsUnregistered(err) || messaging.IsInvalidArgument(err) {
		s.logger.WithField("de_id", de.DEID).Info("FCM token invalid — clearing fcm_token")
		if cerr := s.deRepo.ClearFCMToken(ctx, de.PhoneNumber); cerr != nil {
			s.logger.WithError(cerr).Warn("failed to clear invalid fcm_token")
		}
		return
	}
	s.logger.WithError(err).WithField("de_id", de.DEID).Warn("ORDER_ASSIGNED push failed")
}
