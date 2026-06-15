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

func TestBuildAssignmentMessage_DataOnlyAndroidWithApnsForIOS(t *testing.T) {
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

	// Data-only on Android so the app's FCM background handler can build a
	// full-screen-intent notification via Notifee instead of the OS auto-showing
	// one (you cannot attach a full-screen intent to an OS-rendered notification).
	if msg.Notification != nil {
		t.Errorf("expected no top-level Notification block (data-only), got %+v", msg.Notification)
	}
	if msg.Android == nil {
		t.Fatalf("expected an android config")
	}
	if msg.Android.Priority != "high" {
		t.Errorf("expected android priority high, got %q", msg.Android.Priority)
	}
	if msg.Android.Notification != nil {
		t.Errorf("expected no android notification block (data-only), got %+v", msg.Android.Notification)
	}

	// iOS cannot run JS in the background, so it relies on an APNS-rendered alert
	// with the custom sound; the app takes over (loud loop + sheet) when opened.
	if msg.APNS == nil || msg.APNS.Payload == nil || msg.APNS.Payload.Aps == nil {
		t.Fatalf("expected an APNS payload for iOS")
	}
	if msg.APNS.Payload.Aps.Alert == nil || msg.APNS.Payload.Aps.Alert.Title == "" {
		t.Fatalf("expected an APNS alert with a title")
	}
	if msg.APNS.Payload.Aps.Sound != assignmentSound+".wav" {
		t.Errorf("expected APNS sound %q, got %q", assignmentSound+".wav", msg.APNS.Payload.Aps.Sound)
	}
}
