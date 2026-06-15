package service

import (
	"testing"

	"github.com/qcom/qcom/internal/models"
)

func testTrip() *models.Trip {
	return &models.Trip{
		TripID:     "trip-123",
		OrderID:    "order-456",
		BasePayZMW: 12.5,
	}
}

func TestBuildAssignmentMessage_TargetsTokenWithDataAndChannel(t *testing.T) {
	msg := buildAssignmentMessage("device-token-abc", testTrip())

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
	if msg.Notification == nil || msg.Notification.Title == "" {
		t.Fatalf("expected a notification block with a title")
	}
	if msg.Android == nil || msg.Android.Notification == nil {
		t.Fatalf("expected an android notification config")
	}
	if msg.Android.Notification.ChannelID != assignmentChannelID {
		t.Errorf("expected channel %q, got %q", assignmentChannelID, msg.Android.Notification.ChannelID)
	}
	if msg.Android.Notification.Sound != assignmentSound {
		t.Errorf("expected sound %q, got %q", assignmentSound, msg.Android.Notification.Sound)
	}
}
