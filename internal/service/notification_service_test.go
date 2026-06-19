package service

import (
	"testing"

	"github.com/qcom/qcom/internal/models"
	firebase "firebase.google.com/go/v4/messaging"
)

func testTrip() *models.Trip {
	return &models.Trip{
		TripID:     "trip-123",
		OrderID:    "order-456",
		BasePayZMW: 12.5,
	}
}

func TestBuildAssignmentMessage_HybridTrayWithData(t *testing.T) {
	trip := testTrip()
	trip.AcceptDeadline = "2026-06-15T12:00:00Z"
	msg := buildAssignmentMessage("device-token-abc", trip)

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
	if msg.Android.Notification.ChannelID != assignmentChannelID {
		t.Errorf("expected channel %q, got %q", assignmentChannelID, msg.Android.Notification.ChannelID)
	}
	if msg.Android.Notification.Sound != assignmentSound {
		t.Errorf("expected sound %q, got %q", assignmentSound, msg.Android.Notification.Sound)
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
