//go:build smoke

package smoke

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// assignmentStoreID is the active darkstore this smoke test drives. The
// assignment cron now processes every active darkstore each tick; the DE must
// start duty at this store for the cron to offer its READY_FOR_DELIVERY orders.
const assignmentStoreID = "221"

const (
	tripPollInterval = 5 * time.Second
	tripPollAttempts = 12 // ~60s — assignment cron ticks every 10s
)

// fetchStoreQR returns a live QR code for the given store.
func fetchStoreQR(t *testing.T, storeID string) string {
	t.Helper()
	resp, result := do(t, "GET", "/api/v1/stores/"+storeID+"/qr", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stores/%s/qr: expected 200, got %d: %v", storeID, resp.StatusCode, result)
	}
	qr, _ := result["qr_code"].(string)
	if qr == "" {
		t.Fatalf("stores/%s/qr: empty qr_code", storeID)
	}
	return qr
}

// startDutyOnStore scans the store QR and puts the DE on duty.
func startDutyOnStore(t *testing.T, token, storeID string) {
	t.Helper()
	qr := fetchStoreQR(t, storeID)
	resp, result := do(t, "POST", "/api/v1/de/duty/start", bearer(token), map[string]interface{}{
		"qr_code": qr,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("duty/start: expected 200, got %d: %v", resp.StatusCode, result)
	}
	if result["status"].(string) != "eligible" {
		t.Fatalf("duty/start: expected status=eligible, got %v", result["status"])
	}
	if result["store_id"].(string) != storeID {
		t.Fatalf("duty/start: expected store_id=%s, got %v", storeID, result["store_id"])
	}
}

// pollAssignedTrip waits for the assignment cron to offer a trip to the DE.
// Returns nil if no trip appears within the poll window.
func pollAssignedTrip(t *testing.T, token string) map[string]interface{} {
	t.Helper()
	for i := 0; i < tripPollAttempts; i++ {
		_, result := do(t, "GET", "/api/v1/de/trip", bearer(token), nil)
		if trip, ok := result["trip"].(map[string]interface{}); ok && trip != nil {
			status, _ := trip["status"].(string)
			t.Logf("poll %d/%d: trip_id=%v status=%s", i+1, tripPollAttempts, trip["trip_id"], status)
			if status == "assigned" || status == "accepted" {
				return trip
			}
		}
		if i < tripPollAttempts-1 {
			time.Sleep(tripPollInterval)
		}
	}
	return nil
}

func taskIDs(trip map[string]interface{}) (pickupID, dropID string) {
	tasks, _ := trip["tasks"].([]interface{})
	for _, raw := range tasks {
		task, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		id, _ := task["task_id"].(string)
		switch task["type"] {
		case "pickup":
			pickupID = id
		case "drop":
			dropID = id
		}
	}
	return pickupID, dropID
}

func dropOTP(trip map[string]interface{}) string {
	tasks, _ := trip["tasks"].([]interface{})
	for _, raw := range tasks {
		task, ok := raw.(map[string]interface{})
		if !ok || task["type"] != "drop" {
			continue
		}
		if otp, _ := task["otp"].(string); otp != "" {
			return otp
		}
	}
	return ""
}

func acceptTrip(t *testing.T, token, tripID string) {
	t.Helper()
	resp, result := do(t, "POST", fmt.Sprintf("/api/v1/trip/%s/accept", tripID), bearer(token), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("trip accept: expected 200, got %d: %v", resp.StatusCode, result)
	}
	if result["status"].(string) != "accepted" {
		t.Fatalf("trip accept: expected status=accepted, got %v", result["status"])
	}
}

func updateTask(t *testing.T, token, tripID, taskID string, body map[string]interface{}) {
	t.Helper()
	resp, result := do(t, "POST",
		fmt.Sprintf("/api/v1/trip/%s/task/%s/status/update", tripID, taskID),
		bearer(token), body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("task update %s: expected 200, got %d: %v", taskID, resp.StatusCode, result)
	}
	if result["status"].(string) != "updated" {
		t.Fatalf("task update %s: expected status=updated, got %v", taskID, result["status"])
	}
}

func assertDEStatus(t *testing.T, token, want string) {
	t.Helper()
	resp, result := do(t, "GET", "/api/v1/de/me", bearer(token), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/de/me: expected 200, got %d: %v", resp.StatusCode, result)
	}
	if got := result["status"].(string); got != want {
		t.Fatalf("/de/me: expected status=%s, got %s", want, got)
	}
}

// TestSmoke_DriverOrderLifecycle exercises the full driver delivery path against a
// live environment:
//
//	register → duty start (store 221) → cron assigns trip → accept →
//	pickup complete → drop complete (OTP) → DE returns to free.
//
// Skips when Java has no READY_FOR_DELIVERY orders for store 221 (cross-service dependency).
func TestSmoke_DriverOrderLifecycle(t *testing.T) {
	phone := smokePhone()
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)
	auth := tokens.AccessToken

	// 1. Go on duty at the assignment cron's store.
	startDutyOnStore(t, auth, assignmentStoreID)
	assertDEStatus(t, auth, "eligible")

	// 2. Wait for the cron to assign a READY_FOR_DELIVERY order.
	trip := pollAssignedTrip(t, auth)
	if trip == nil {
		t.Skip("no trip assigned within poll window — ensure Java has a READY_FOR_DELIVERY order for store " + assignmentStoreID)
	}

	tripID, _ := trip["trip_id"].(string)
	orderID, _ := trip["order_id"].(string)
	t.Logf("assigned trip_id=%s order_id=%s status=%v", tripID, orderID, trip["status"])

	pickupID, dropID := taskIDs(trip)
	if pickupID == "" || dropID == "" {
		t.Fatalf("trip missing pickup/drop tasks: pickup=%q drop=%q", pickupID, dropID)
	}

	// 3. Accept the trip (required before pickup in the accept/reject flow).
	if status, _ := trip["status"].(string); status == "assigned" {
		acceptTrip(t, auth, tripID)
	}

	// 4. Complete pickup → trip moves to out_for_delivery.
	updateTask(t, auth, tripID, pickupID, map[string]interface{}{"status": "completed"})

	// 5. Complete drop with customer OTP → trip completed, DE freed.
	otp := dropOTP(trip)
	if otp == "" {
		// Re-fetch in case OTP was not present on the assigned snapshot.
		_, refreshed := do(t, "GET", "/api/v1/de/trip", bearer(auth), nil)
		if tr, ok := refreshed["trip"].(map[string]interface{}); ok {
			otp = dropOTP(tr)
		}
	}
	if otp == "" {
		t.Fatal("drop task missing OTP — cannot complete delivery")
	}
	updateTask(t, auth, tripID, dropID, map[string]interface{}{
		"status": "completed",
		"otp":    otp,
	})

	// 6. DE is freed; no active trip.
	assertDEStatus(t, auth, "free")
	resp, result := do(t, "GET", "/api/v1/de/trip", bearer(auth), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /de/trip after completion: expected 200, got %d", resp.StatusCode)
	}
	if result["trip"] != nil {
		t.Fatalf("expected no active trip after completion, got %v", result["trip"])
	}

	t.Logf("driver order lifecycle complete: order_id=%s trip_id=%s", orderID, tripID)
}
