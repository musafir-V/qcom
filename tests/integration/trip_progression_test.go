//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestTripProgressionFlow covers the full DE journey:
// register → start duty → trip assigned by cron → pickup complete → drop complete → DE free.
//
// Requires a running server (testServer harness), DynamoDB, and a Java
// order-service with at least one PACKING order for the test store so the
// assignment cron can create and assign a trip. When no trip materialises
// within the poll window the test skips, since the cross-service setup is
// not guaranteed in every environment.
func TestTripProgressionFlow(t *testing.T) {
	// 1. Register and authenticate a DE.
	phone := uniquePhone("90")
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)
	auth := bearerHeaders(tokens.AccessToken)

	// 2. Start duty (scan QR for the test store).
	qrCode := currentQRCode(t, testStoreID)
	resp, result := doRequest(t, "POST", "/api/v1/de/duty/start", auth,
		map[string]interface{}{"qr_code": qrCode})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("duty/start: expected 200, got %d: %v", resp.StatusCode, result)
	}

	// 3. Poll for a trip assigned by the cron (up to ~30s).
	var trip map[string]interface{}
	for i := 0; i < 6; i++ {
		_, tripResult := doRequest(t, "GET", "/api/v1/de/trip", auth, nil)
		if tr, ok := tripResult["trip"].(map[string]interface{}); ok {
			trip = tr
			break
		}
		time.Sleep(5 * time.Second)
	}
	if trip == nil {
		t.Skip("no trip assigned within poll window — ensure Java has a PACKING order for the test store")
	}

	tripID, _ := trip["trip_id"].(string)
	tasks, _ := trip["tasks"].([]interface{})

	// Find pickup and drop task IDs.
	var pickupTaskID, dropTaskID string
	for _, raw := range tasks {
		task, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if task["type"] == "pickup" {
			pickupTaskID, _ = task["task_id"].(string)
		} else {
			dropTaskID, _ = task["task_id"].(string)
		}
	}

	// 4. Complete pickup.
	resp, result = doRequest(t, "POST",
		fmt.Sprintf("/api/v1/trip/%s/task/%s/status/update", tripID, pickupTaskID),
		auth, map[string]interface{}{"status": "completed"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete pickup: expected 200, got %d: %v", resp.StatusCode, result)
	}

	// 5. Complete drop.
	resp, result = doRequest(t, "POST",
		fmt.Sprintf("/api/v1/trip/%s/task/%s/status/update", tripID, dropTaskID),
		auth, map[string]interface{}{"status": "completed"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete drop: expected 200, got %d: %v", resp.StatusCode, result)
	}

	// 6. DE is freed atomically with trip completion.
	assertDEStatus(t, phone, "free")
}
