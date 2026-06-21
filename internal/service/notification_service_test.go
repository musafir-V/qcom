package service

import (
	"testing"

	"github.com/qcom/qcom/internal/models"
	firebase "firebase.google.com/go/v4/messaging"
)

func TestBuildFCMMessage_OrderAssigned(t *testing.T) {
	req := models.NotificationSendRequest{
		RecipientType: models.RecipientTypeDriver,
		RecipientID:   "DE-1",
		EventType:     "ORDER_ASSIGNED",
		Priority:      models.PriorityCritical,
		Title:         "New order!",
		Body:          "Tap to view your trip.",
		Data: map[string]string{
			"trip_id":         "trip-123",
			"order_id":        "order-456",
			"accept_deadline": "2026-06-15T12:00:00Z",
		},
	}

	msg := buildFCMMessage("device-token-abc", req)

	if msg.Token != "device-token-abc" {
		t.Fatalf("expected token device-token-abc, got %q", msg.Token)
	}
	if msg.Data["type"] != "ORDER_ASSIGNED" {
		t.Errorf("expected data.type ORDER_ASSIGNED, got %q", msg.Data["type"])
	}
	if msg.Notification == nil || msg.Notification.Title != "New order!" {
		t.Fatalf("expected tray notification title New order!, got %+v", msg.Notification)
	}
	if msg.Android == nil || msg.Android.Notification == nil {
		t.Fatalf("expected android notification block")
	}
	if msg.Android.Notification.ChannelID != driverChannelID {
		t.Errorf("expected channel %q, got %q", driverChannelID, msg.Android.Notification.ChannelID)
	}
	if msg.Android.Notification.Sound != "" {
		t.Errorf("expected no custom sound, got %q", msg.Android.Notification.Sound)
	}
	if msg.Android.Notification.Tag != "trip-123" {
		t.Errorf("expected tag trip-123, got %q", msg.Android.Notification.Tag)
	}
	if msg.Android.Notification.Priority != firebase.PriorityHigh {
		t.Errorf("expected high notification priority, got %v", msg.Android.Notification.Priority)
	}
	if msg.APNS.Headers["apns-collapse-id"] != "trip-123" {
		t.Errorf("expected apns-collapse-id trip-123, got %q", msg.APNS.Headers["apns-collapse-id"])
	}
}

func TestValidateSendRequest_RejectsLowPriorityForOrderAssigned(t *testing.T) {
	req := models.NotificationSendRequest{
		RecipientType: models.RecipientTypeDriver,
		RecipientID:   "DE-1",
		EventType:     "ORDER_ASSIGNED",
		Priority:      models.PriorityNormal,
		Title:         "New order!",
		Body:          "Tap to view your trip.",
	}
	if err := validateSendRequest(req); err == nil {
		t.Fatal("expected validation error for ORDER_ASSIGNED with normal priority")
	}
}

func TestRecipientTypeFromJWT(t *testing.T) {
	driver, err := RecipientTypeFromJWT("de")
	if err != nil || driver != models.RecipientTypeDriver {
		t.Fatalf("expected driver, got %v err=%v", driver, err)
	}
	customer, err := RecipientTypeFromJWT("customer")
	if err != nil || customer != models.RecipientTypeCustomer {
		t.Fatalf("expected customer, got %v err=%v", customer, err)
	}
	if _, err := RecipientTypeFromJWT("guest"); err == nil {
		t.Fatal("expected error for guest entity type")
	}
}
