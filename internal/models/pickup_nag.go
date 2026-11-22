package models

import (
	"strings"
	"time"
)

const (
	PickupNagDelay        = time.Minute
	PickupNagPushInterval = 30 * time.Second
	PickupNagEvent        = "PICKUP_NAG"
	PickupNagTitle        = "Order ready"
	PickupNagFallbackBody = "Pick up your order"
)

// PickupNagAt is one minute after the later of accept and ready.
// A zero time means the clock has not started.
func PickupNagAt(acceptedAt, readyAt time.Time) time.Time {
	if acceptedAt.IsZero() || readyAt.IsZero() {
		return time.Time{}
	}
	later := acceptedAt
	if readyAt.After(later) {
		later = readyAt
	}
	return later.Add(PickupNagDelay)
}

// PickupNagBody is the lock-screen line: the rack label, or a fixed fallback.
func PickupNagBody(deliveryZone string) string {
	zone := strings.TrimSpace(deliveryZone)
	if zone == "" {
		return PickupNagFallbackBody
	}
	return zone
}

// RefreshPickupNag fills pickup_nag_at on every pickup from accepted_at and ready_at.
func (t *Trip) RefreshPickupNag() {
	if t == nil {
		return
	}
	accepted, okA := parseRFC3339(t.AcceptedAt)
	for i := range t.Tasks {
		if t.Tasks[i].Type != TaskTypePickup {
			continue
		}
		ready, okR := parseRFC3339(t.Tasks[i].ReadyAt)
		nag := PickupNagAt(accepted, ready)
		if !okA || !okR || nag.IsZero() {
			t.Tasks[i].PickupNagAt = ""
			continue
		}
		t.Tasks[i].PickupNagAt = nag.Format(time.RFC3339)
	}
}

// MarkOrderReady records the first time this order was seen as ready.
// A later call keeps the original ready_at. Returns true when it stamped.
func (t *Trip) MarkOrderReady(orderID string, at time.Time) bool {
	if t == nil || at.IsZero() {
		return false
	}
	pickup := t.PickupForOrder(orderID)
	if pickup == nil {
		return false
	}
	stamped := false
	if pickup.ReadyAt == "" {
		pickup.ReadyAt = at.Format(time.RFC3339)
		stamped = true
	}
	t.RefreshPickupNag()
	return stamped
}

// PickupNagNotification is the critical push for the pickup the rider is on,
// once its minute has passed, and at most once every 30 seconds.
func (t *Trip) PickupNagNotification(now time.Time) (NotificationSendRequest, bool) {
	if t == nil || t.Status != TripStatusAccepted || t.DEID == "" {
		return NotificationSendRequest{}, false
	}
	t.RefreshPickupNag()
	cur := t.FirstIncompleteTask()
	if cur == nil || cur.Type != TaskTypePickup || cur.Status == TaskStatusCompleted {
		return NotificationSendRequest{}, false
	}
	nag, ok := parseRFC3339(cur.PickupNagAt)
	if !ok || now.Before(nag) {
		return NotificationSendRequest{}, false
	}
	if last, ok := parseRFC3339(t.PickupNagPushAt); ok && now.Before(last.Add(PickupNagPushInterval)) {
		return NotificationSendRequest{}, false
	}
	body := PickupNagBody(cur.DeliveryZone)
	orderID := t.TaskOrderID(cur)
	return NotificationSendRequest{
		RecipientType: RecipientTypeDriver,
		RecipientID:   t.DEID,
		EventType:     PickupNagEvent,
		Priority:      PriorityCritical,
		Title:         PickupNagTitle,
		Body:          body,
		Data: map[string]string{
			"type":     PickupNagEvent,
			"trip_id":  t.TripID,
			"order_id": orderID,
			"title":    PickupNagTitle,
			"body":     body,
		},
	}, true
}

func parseRFC3339(value string) (time.Time, bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
