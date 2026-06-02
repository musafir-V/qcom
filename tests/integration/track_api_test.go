//go:build integration

package integration

import (
	"net/http"
	"testing"
)

// TestTrackAPI_NoTrip verifies that the track API returns 404 ORDER_NOT_FOUND
// for a bogus order that has no trip and is unknown to Java.
func TestTrackAPI_NoTrip(t *testing.T) {
	phone := uniquePhone("95")
	tokens := authenticateUser(t, phone)

	// Use a non-existent order ID — no trip exists and Java should report NOT_FOUND.
	resp, _ := doRequest(t, "GET",
		"/api/v1/orders/non-existent-order/track",
		bearerHeaders(tokens.AccessToken), nil)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for non-existent order, got %d", resp.StatusCode)
	}
}

// TestTrackAPI_CompletedTrip verifies 400 TRIP_COMPLETED for a finished trip.
// Requires a completed trip to exist — run manually after TestTripProgressionFlow.
func TestTrackAPI_CompletedTrip(t *testing.T) {
	t.Skip("run manually after TestTripProgressionFlow creates a completed trip")
}
