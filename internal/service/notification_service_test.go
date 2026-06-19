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

func TestBuildAssignmentMessage_HybridPayloadWithTrayAndData(t *testing.T) {
	trip := testTrip()
	trip.AcceptDeadline = "2026-06-15T12:00:00Z"
	msg := buildAssignmentMessage("device-token-abc", trip)

	if msg.Token != "device-token-abc" {
		t.Fatalf("expected token device-token-abc, got %q", msg.Token)
	}
	if msg.Data["type"] != "ORDER_ASSIGNED" {
		t.Errorf("expected data.type ORDER_ASSIGNED, got %q", msg.Data["type"])
	}
	if msg.Data["trip_id"] != "trip-123" {
		t.Errorf("expected data.trip_id trip-123, got %q", msg.Data["trip_id"])
	}
	if msg.Data["order_id"] != "order-456" {
		t.Errorf("expected data.order_id order-456, got %q", msg.Data["order_id"])
	}
	if msg.Data["accept_deadline"] != "2026-06-15T12:00:00Z" {
		t.Errorf("expected data.accept_deadline carried through, got %q", msg.Data["accept_deadline"])
	}

	if msg.Notification == nil || msg.Notification.Title != "New order!" {
		t.Fatalf("expected top-level notification title New order!, got %+v", msg.Notification)
	}

	if msg.Android == nil {
		t.Fatalf("expected an android config")
	}
	if msg.Android.Priority != "high" {
		t.Errorf("expected android priority high, got %q", msg.Android.Priority)
	}
	if msg.Android.Notification == nil {
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
		t.Errorf("expected android notification priority high, got %v", msg.Android.Notification.Priority)
	}

	if msg.APNS == nil || msg.APNS.Payload == nil || msg.APNS.Payload.Aps == nil {
		t.Fatalf("expected an APNS payload for iOS")
	}
	if msg.APNS.Payload.Aps.Alert == nil || msg.APNS.Payload.Aps.Alert.Title == "" {
		t.Fatalf("expected an APNS alert with a title")
	}
	if msg.APNS.Payload.Aps.Sound != assignmentSound+".wav" {
		t.Errorf("expected APNS sound %q, got %q", assignmentSound+".wav", msg.APNS.Payload.Aps.Sound)
	}
	if msg.APNS.Headers["apns-collapse-id"] != "trip-123" {
		t.Errorf("expected apns-collapse-id trip-123, got %q", msg.APNS.Headers["apns-collapse-id"])
	}
}
