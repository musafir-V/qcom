//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
)

// ── helpers ──────────────────────────────────────────────────────────────

func doAddressRequest(t *testing.T, method, path, token string, body interface{}) (*http.Response, map[string]interface{}) {
	t.Helper()

	var reqBody io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reqBody = bytes.NewReader(b)
	}

	req, _ := http.NewRequest(method, testServer.URL+path, reqBody)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request %s %s failed: %v", method, path, err)
	}

	var result map[string]interface{}
	respBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	json.Unmarshal(respBytes, &result)

	return resp, result
}

func createTestAddress(t *testing.T, token string, overrides map[string]interface{}) (map[string]interface{}, string) {
	t.Helper()

	body := map[string]interface{}{
		"receiver_name":      "Test User",
		"receiver_phone":     "+919876543210",
		"building_and_floor": "Tower B, 4th Floor",
		"address_line_1":     "Sector 62, Noida",
		"address_line_2":     "Near Metro",
		"latitude":           28.627235,
		"longitude":          77.364715,
		"label":              "home",
	}
	for k, v := range overrides {
		body[k] = v
	}

	resp, result := doAddressRequest(t, "POST", "/api/v1/addresses", token, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("failed to create test address: %d %v", resp.StatusCode, result)
	}

	data := result["data"].(map[string]interface{})
	return data, data["address_id"].(string)
}

// ── Create Address Tests ─────────────────────────────────────────────────

func TestCreateAddress_Success(t *testing.T) {
	auth := authenticateUser(t, "+12000000001")

	resp, result := doAddressRequest(t, "POST", "/api/v1/addresses", auth.AccessToken, map[string]interface{}{
		"receiver_name":      "Shivang",
		"receiver_phone":     "+919876543210",
		"building_and_floor": "Tower B, 4th Floor",
		"address_line_1":     "Sector 62, Noida",
		"address_line_2":     "Near Metro",
		"latitude":           28.627235,
		"longitude":          77.364715,
		"label":              "home",
	})

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %v", resp.StatusCode, result)
	}

	data := result["data"].(map[string]interface{})
	if data["address_id"] == nil || data["address_id"].(string) == "" {
		t.Fatal("missing address_id")
	}
	if data["user_id"].(string) != auth.UserID {
		t.Fatalf("user_id mismatch: got %s, want %s", data["user_id"], auth.UserID)
	}
	if data["receiver_name"].(string) != "Shivang" {
		t.Fatalf("receiver_name mismatch")
	}
	if data["is_active"].(bool) != true {
		t.Fatal("expected is_active=true")
	}
	if data["label"].(string) != "home" {
		t.Fatalf("label mismatch")
	}
}

func TestCreateAddress_DefaultLabel(t *testing.T) {
	auth := authenticateUser(t, "+12000000002")

	resp, result := doAddressRequest(t, "POST", "/api/v1/addresses", auth.AccessToken, map[string]interface{}{
		"receiver_name":      "Test",
		"receiver_phone":     "+919876543210",
		"building_and_floor": "House 1",
		"address_line_1":     "Street 1",
		"latitude":           28.0,
		"longitude":          77.0,
	})

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %v", resp.StatusCode, result)
	}

	data := result["data"].(map[string]interface{})
	if data["label"].(string) != "other" {
		t.Fatalf("expected default label 'other', got %s", data["label"])
	}
}

func TestCreateAddress_MissingReceiverName(t *testing.T) {
	auth := authenticateUser(t, "+12000000003")

	resp, result := doAddressRequest(t, "POST", "/api/v1/addresses", auth.AccessToken, map[string]interface{}{
		"receiver_phone":     "+919876543210",
		"building_and_floor": "House 1",
		"address_line_1":     "Street 1",
		"latitude":           28.0,
		"longitude":          77.0,
	})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"] != "MISSING_FIELD" {
		t.Fatalf("expected MISSING_FIELD, got %v", errObj["code"])
	}
}

func TestCreateAddress_MissingBuildingAndFloor(t *testing.T) {
	auth := authenticateUser(t, "+12000000004")

	resp, result := doAddressRequest(t, "POST", "/api/v1/addresses", auth.AccessToken, map[string]interface{}{
		"receiver_name":  "Test",
		"receiver_phone": "+919876543210",
		"address_line_1": "Street 1",
		"latitude":       28.0,
		"longitude":      77.0,
	})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"] != "MISSING_FIELD" {
		t.Fatalf("expected MISSING_FIELD, got %v", errObj["code"])
	}
}

func TestCreateAddress_InvalidPhone(t *testing.T) {
	auth := authenticateUser(t, "+12000000005")

	resp, result := doAddressRequest(t, "POST", "/api/v1/addresses", auth.AccessToken, map[string]interface{}{
		"receiver_name":      "Test",
		"receiver_phone":     "not-a-phone",
		"building_and_floor": "House 1",
		"address_line_1":     "Street 1",
		"latitude":           28.0,
		"longitude":          77.0,
	})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"] != "INVALID_PHONE" {
		t.Fatalf("expected INVALID_PHONE, got %v", errObj["code"])
	}
}

func TestCreateAddress_InvalidCoordinates(t *testing.T) {
	auth := authenticateUser(t, "+12000000006")

	resp, result := doAddressRequest(t, "POST", "/api/v1/addresses", auth.AccessToken, map[string]interface{}{
		"receiver_name":      "Test",
		"receiver_phone":     "+919876543210",
		"building_and_floor": "House 1",
		"address_line_1":     "Street 1",
		"latitude":           91.0,
		"longitude":          77.0,
	})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"] != "INVALID_COORDINATES" {
		t.Fatalf("expected INVALID_COORDINATES, got %v", errObj["code"])
	}
}

func TestCreateAddress_InvalidLabel(t *testing.T) {
	auth := authenticateUser(t, "+12000000007")

	resp, result := doAddressRequest(t, "POST", "/api/v1/addresses", auth.AccessToken, map[string]interface{}{
		"receiver_name":      "Test",
		"receiver_phone":     "+919876543210",
		"building_and_floor": "House 1",
		"address_line_1":     "Street 1",
		"latitude":           28.0,
		"longitude":          77.0,
		"label":              "garage",
	})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"] != "INVALID_LABEL" {
		t.Fatalf("expected INVALID_LABEL, got %v", errObj["code"])
	}
}

func TestCreateAddress_NoAuth(t *testing.T) {
	resp, _ := doAddressRequest(t, "POST", "/api/v1/addresses", "", map[string]interface{}{
		"receiver_name":      "Test",
		"receiver_phone":     "+919876543210",
		"building_and_floor": "House 1",
		"address_line_1":     "Street 1",
		"latitude":           28.0,
		"longitude":          77.0,
	})

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

// ── Get Address by ID Tests ──────────────────────────────────────────────

func TestGetAddressByID_Success(t *testing.T) {
	auth := authenticateUser(t, "+12000000010")
	_, addrID := createTestAddress(t, auth.AccessToken, nil)

	resp, result := doAddressRequest(t, "GET", "/api/v1/addresses/"+addrID, auth.AccessToken, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}

	data := result["data"].(map[string]interface{})
	if data["address_id"].(string) != addrID {
		t.Fatalf("address_id mismatch")
	}
}

func TestGetAddressByID_NotFound(t *testing.T) {
	auth := authenticateUser(t, "+12000000011")

	resp, result := doAddressRequest(t, "GET", "/api/v1/addresses/00000000-0000-0000-0000-000000000000", auth.AccessToken, nil)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %v", resp.StatusCode, result)
	}
}

func TestGetAddressByID_InvalidUUID(t *testing.T) {
	auth := authenticateUser(t, "+12000000012")

	resp, result := doAddressRequest(t, "GET", "/api/v1/addresses/not-a-uuid", auth.AccessToken, nil)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"] != "INVALID_ADDRESS_ID" {
		t.Fatalf("expected INVALID_ADDRESS_ID, got %v", errObj["code"])
	}
}

func TestGetAddressByID_Forbidden(t *testing.T) {
	auth1 := authenticateUser(t, "+12000000013")
	auth2 := authenticateUser(t, "+12000000014")

	_, addrID := createTestAddress(t, auth1.AccessToken, nil)

	resp, result := doAddressRequest(t, "GET", "/api/v1/addresses/"+addrID, auth2.AccessToken, nil)

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %v", resp.StatusCode, result)
	}
}

// ── Get My Addresses Tests ───────────────────────────────────────────────

func TestGetMyAddresses_Success(t *testing.T) {
	auth := authenticateUser(t, "+12000000020")
	createTestAddress(t, auth.AccessToken, map[string]interface{}{"label": "home"})
	createTestAddress(t, auth.AccessToken, map[string]interface{}{
		"building_and_floor": "House 2",
		"label":              "work",
	})

	resp, result := doAddressRequest(t, "GET", "/api/v1/addresses", auth.AccessToken, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}

	data := result["data"].([]interface{})
	if len(data) != 2 {
		t.Fatalf("expected 2 addresses, got %d", len(data))
	}

	pagination := result["pagination"].(map[string]interface{})
	if int(pagination["count"].(float64)) != 2 {
		t.Fatalf("expected count=2, got %v", pagination["count"])
	}
}

func TestGetMyAddresses_Empty(t *testing.T) {
	auth := authenticateUser(t, "+12000000021")

	resp, result := doAddressRequest(t, "GET", "/api/v1/addresses", auth.AccessToken, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	data := result["data"].([]interface{})
	if len(data) != 0 {
		t.Fatalf("expected 0 addresses, got %d", len(data))
	}
}

func TestGetMyAddresses_ExcludesSoftDeleted(t *testing.T) {
	auth := authenticateUser(t, "+12000000022")
	_, addrID := createTestAddress(t, auth.AccessToken, nil)
	createTestAddress(t, auth.AccessToken, map[string]interface{}{"building_and_floor": "House 2"})

	// Soft-delete the first address
	doAddressRequest(t, "DELETE", "/api/v1/addresses/"+addrID, auth.AccessToken, nil)

	resp, result := doAddressRequest(t, "GET", "/api/v1/addresses", auth.AccessToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	data := result["data"].([]interface{})
	if len(data) != 1 {
		t.Fatalf("expected 1 address after soft-delete, got %d", len(data))
	}
}

// ── Update Receiver Details Tests ────────────────────────────────────────

func TestUpdateReceiverDetails_Success(t *testing.T) {
	auth := authenticateUser(t, "+12000000030")
	_, addrID := createTestAddress(t, auth.AccessToken, nil)

	resp, result := doAddressRequest(t, "PATCH", "/api/v1/addresses/"+addrID, auth.AccessToken, map[string]interface{}{
		"receiver_name":  "New Name",
		"receiver_phone": "+911234567890",
	})

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}

	data := result["data"].(map[string]interface{})
	if data["receiver_name"].(string) != "New Name" {
		t.Fatalf("receiver_name not updated")
	}
	if data["receiver_phone"].(string) != "+911234567890" {
		t.Fatalf("receiver_phone not updated")
	}
	// Location fields unchanged
	if data["building_and_floor"].(string) != "Tower B, 4th Floor" {
		t.Fatal("building_and_floor should not have changed")
	}
}

func TestUpdateReceiverDetails_NameOnly(t *testing.T) {
	auth := authenticateUser(t, "+12000000031")
	_, addrID := createTestAddress(t, auth.AccessToken, nil)

	resp, result := doAddressRequest(t, "PATCH", "/api/v1/addresses/"+addrID, auth.AccessToken, map[string]interface{}{
		"receiver_name": "Only Name Changed",
	})

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}

	data := result["data"].(map[string]interface{})
	if data["receiver_name"].(string) != "Only Name Changed" {
		t.Fatalf("receiver_name not updated")
	}
	if data["receiver_phone"].(string) != "+919876543210" {
		t.Fatal("receiver_phone should not have changed")
	}
}

func TestUpdateReceiverDetails_EmptyBody(t *testing.T) {
	auth := authenticateUser(t, "+12000000032")
	_, addrID := createTestAddress(t, auth.AccessToken, nil)

	resp, result := doAddressRequest(t, "PATCH", "/api/v1/addresses/"+addrID, auth.AccessToken, map[string]interface{}{})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"] != "EMPTY_UPDATE" {
		t.Fatalf("expected EMPTY_UPDATE, got %v", errObj["code"])
	}
}

func TestUpdateReceiverDetails_InvalidPhone(t *testing.T) {
	auth := authenticateUser(t, "+12000000033")
	_, addrID := createTestAddress(t, auth.AccessToken, nil)

	resp, result := doAddressRequest(t, "PATCH", "/api/v1/addresses/"+addrID, auth.AccessToken, map[string]interface{}{
		"receiver_phone": "bad-phone",
	})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"] != "INVALID_PHONE" {
		t.Fatalf("expected INVALID_PHONE, got %v", errObj["code"])
	}
}

func TestUpdateReceiverDetails_Forbidden(t *testing.T) {
	auth1 := authenticateUser(t, "+12000000034")
	auth2 := authenticateUser(t, "+12000000035")

	_, addrID := createTestAddress(t, auth1.AccessToken, nil)

	resp, _ := doAddressRequest(t, "PATCH", "/api/v1/addresses/"+addrID, auth2.AccessToken, map[string]interface{}{
		"receiver_name": "Hacker",
	})

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestUpdateReceiverDetails_NotFound(t *testing.T) {
	auth := authenticateUser(t, "+12000000036")

	resp, _ := doAddressRequest(t, "PATCH", "/api/v1/addresses/00000000-0000-0000-0000-000000000000", auth.AccessToken, map[string]interface{}{
		"receiver_name": "Ghost",
	})

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

// ── Remove Address Tests ─────────────────────────────────────────────────

func TestRemoveAddress_Success(t *testing.T) {
	auth := authenticateUser(t, "+12000000040")
	_, addrID := createTestAddress(t, auth.AccessToken, nil)

	resp, result := doAddressRequest(t, "DELETE", "/api/v1/addresses/"+addrID, auth.AccessToken, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}
	if result["message"].(string) != "Address removed successfully" {
		t.Fatalf("unexpected message: %v", result["message"])
	}

	// Verify it's no longer fetchable
	resp2, _ := doAddressRequest(t, "GET", "/api/v1/addresses/"+addrID, auth.AccessToken, nil)
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after soft-delete, got %d", resp2.StatusCode)
	}
}

func TestRemoveAddress_AlreadyDeleted(t *testing.T) {
	auth := authenticateUser(t, "+12000000041")
	_, addrID := createTestAddress(t, auth.AccessToken, nil)

	doAddressRequest(t, "DELETE", "/api/v1/addresses/"+addrID, auth.AccessToken, nil)

	// Try deleting again
	resp, _ := doAddressRequest(t, "DELETE", "/api/v1/addresses/"+addrID, auth.AccessToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for already-deleted, got %d", resp.StatusCode)
	}
}

func TestRemoveAddress_Forbidden(t *testing.T) {
	auth1 := authenticateUser(t, "+12000000042")
	auth2 := authenticateUser(t, "+12000000043")

	_, addrID := createTestAddress(t, auth1.AccessToken, nil)

	resp, _ := doAddressRequest(t, "DELETE", "/api/v1/addresses/"+addrID, auth2.AccessToken, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestRemoveAddress_NotFound(t *testing.T) {
	auth := authenticateUser(t, "+12000000044")

	resp, _ := doAddressRequest(t, "DELETE", "/api/v1/addresses/00000000-0000-0000-0000-000000000000", auth.AccessToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

// ── Suggest Addresses Tests ──────────────────────────────────────────────

func TestSuggestAddresses_ReturnsNearby(t *testing.T) {
	auth := authenticateUser(t, "+12000000050")

	// Create address at exact location
	createTestAddress(t, auth.AccessToken, map[string]interface{}{
		"latitude":  28.627235,
		"longitude": 77.364715,
		"label":     "home",
	})

	// Query from the same coordinates
	resp, result := doAddressRequest(t, "GET",
		"/api/v1/addresses/suggest?latitude=28.627235&longitude=77.364715",
		auth.AccessToken, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}

	data := result["data"].([]interface{})
	if len(data) != 1 {
		t.Fatalf("expected 1 suggested address, got %d", len(data))
	}

	first := data[0].(map[string]interface{})
	distMeters := first["distance_meters"].(float64)
	if distMeters > 1.0 {
		t.Fatalf("expected ~0m distance for same coords, got %.1f", distMeters)
	}
}

func TestSuggestAddresses_ExcludesFarAway(t *testing.T) {
	auth := authenticateUser(t, "+12000000051")

	// Noida
	createTestAddress(t, auth.AccessToken, map[string]interface{}{
		"latitude":  28.627235,
		"longitude": 77.364715,
	})

	// Query from Delhi (~30km away)
	resp, result := doAddressRequest(t, "GET",
		"/api/v1/addresses/suggest?latitude=28.6139&longitude=77.2090",
		auth.AccessToken, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	data := result["data"].([]interface{})
	if len(data) != 0 {
		t.Fatalf("expected 0 suggestions for far-away point, got %d", len(data))
	}
}

func TestSuggestAddresses_SortedByDistance(t *testing.T) {
	auth := authenticateUser(t, "+12000000052")

	// Create two addresses very close together
	// Point A: exact query location
	createTestAddress(t, auth.AccessToken, map[string]interface{}{
		"building_and_floor": "Building A",
		"latitude":           28.627235,
		"longitude":          77.364715,
	})
	// Point B: ~50m away
	createTestAddress(t, auth.AccessToken, map[string]interface{}{
		"building_and_floor": "Building B",
		"latitude":           28.627685,
		"longitude":          77.364715,
	})

	resp, result := doAddressRequest(t, "GET",
		"/api/v1/addresses/suggest?latitude=28.627235&longitude=77.364715",
		auth.AccessToken, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	data := result["data"].([]interface{})
	if len(data) != 2 {
		t.Fatalf("expected 2 suggestions, got %d", len(data))
	}

	d0 := data[0].(map[string]interface{})["distance_meters"].(float64)
	d1 := data[1].(map[string]interface{})["distance_meters"].(float64)
	if d0 > d1 {
		t.Fatalf("expected sorted ascending: %.1f > %.1f", d0, d1)
	}
}

func TestSuggestAddresses_MissingLatitude(t *testing.T) {
	auth := authenticateUser(t, "+12000000053")

	resp, result := doAddressRequest(t, "GET",
		"/api/v1/addresses/suggest?longitude=77.0",
		auth.AccessToken, nil)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}
}

func TestSuggestAddresses_InvalidLatitude(t *testing.T) {
	auth := authenticateUser(t, "+12000000054")

	resp, result := doAddressRequest(t, "GET",
		"/api/v1/addresses/suggest?latitude=999&longitude=77.0",
		auth.AccessToken, nil)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"] != "INVALID_COORDINATES" {
		t.Fatalf("expected INVALID_COORDINATES, got %v", errObj["code"])
	}
}

func TestSuggestAddresses_EmptyResult(t *testing.T) {
	auth := authenticateUser(t, "+12000000055")

	resp, result := doAddressRequest(t, "GET",
		"/api/v1/addresses/suggest?latitude=28.0&longitude=77.0",
		auth.AccessToken, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	data := result["data"].([]interface{})
	if len(data) != 0 {
		t.Fatalf("expected 0 suggestions, got %d", len(data))
	}

	count := int(result["count"].(float64))
	if count != 0 {
		t.Fatalf("expected count=0, got %d", count)
	}
}

func TestSuggestAddresses_ExcludesSoftDeleted(t *testing.T) {
	auth := authenticateUser(t, "+12000000056")

	_, addrID := createTestAddress(t, auth.AccessToken, map[string]interface{}{
		"latitude":  28.627235,
		"longitude": 77.364715,
	})

	// Delete the address
	doAddressRequest(t, "DELETE", "/api/v1/addresses/"+addrID, auth.AccessToken, nil)

	// Suggest from same coords
	resp, result := doAddressRequest(t, "GET",
		"/api/v1/addresses/suggest?latitude=28.627235&longitude=77.364715",
		auth.AccessToken, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	data := result["data"].([]interface{})
	if len(data) != 0 {
		t.Fatalf("expected 0 suggestions after soft-delete, got %d", len(data))
	}
}

// ── Cross-user isolation ─────────────────────────────────────────────────

func TestCrossUserIsolation(t *testing.T) {
	auth1 := authenticateUser(t, "+12000000060")
	auth2 := authenticateUser(t, "+12000000061")

	createTestAddress(t, auth1.AccessToken, map[string]interface{}{"label": "home"})
	createTestAddress(t, auth1.AccessToken, map[string]interface{}{"label": "work"})
	createTestAddress(t, auth2.AccessToken, map[string]interface{}{"label": "home"})

	// User1 should see 2
	resp1, result1 := doAddressRequest(t, "GET", "/api/v1/addresses", auth1.AccessToken, nil)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp1.StatusCode)
	}
	data1 := result1["data"].([]interface{})
	if len(data1) != 2 {
		t.Fatalf("user1 expected 2 addresses, got %d", len(data1))
	}

	// User2 should see 1
	resp2, result2 := doAddressRequest(t, "GET", "/api/v1/addresses", auth2.AccessToken, nil)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}
	data2 := result2["data"].([]interface{})
	if len(data2) != 1 {
		t.Fatalf("user2 expected 1 address, got %d", len(data2))
	}
}

// ── End-to-end lifecycle test ────────────────────────────────────────────

func TestAddressLifecycle(t *testing.T) {
	auth := authenticateUser(t, "+12000000070")

	// 1. Create
	_, addrID := createTestAddress(t, auth.AccessToken, nil)
	t.Logf("Created address: %s", addrID)

	// 2. Get by ID
	resp, result := doAddressRequest(t, "GET", "/api/v1/addresses/"+addrID, auth.AccessToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET by ID: expected 200, got %d", resp.StatusCode)
	}

	// 3. List
	resp, result = doAddressRequest(t, "GET", "/api/v1/addresses", auth.AccessToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET list: expected 200, got %d", resp.StatusCode)
	}
	data := result["data"].([]interface{})
	if len(data) != 1 {
		t.Fatalf("expected 1 address in list, got %d", len(data))
	}

	// 4. Patch receiver details
	resp, result = doAddressRequest(t, "PATCH", "/api/v1/addresses/"+addrID, auth.AccessToken, map[string]interface{}{
		"receiver_name": "Updated Name",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH: expected 200, got %d", resp.StatusCode)
	}
	patchedData := result["data"].(map[string]interface{})
	if patchedData["receiver_name"].(string) != "Updated Name" {
		t.Fatal("receiver_name not updated after PATCH")
	}

	// 5. Suggest (same coords should return the address)
	resp, result = doAddressRequest(t, "GET",
		fmt.Sprintf("/api/v1/addresses/suggest?latitude=%f&longitude=%f", 28.627235, 77.364715),
		auth.AccessToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("suggest: expected 200, got %d", resp.StatusCode)
	}
	suggested := result["data"].([]interface{})
	if len(suggested) != 1 {
		t.Fatalf("expected 1 suggestion, got %d", len(suggested))
	}

	// 6. Remove
	resp, _ = doAddressRequest(t, "DELETE", "/api/v1/addresses/"+addrID, auth.AccessToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: expected 200, got %d", resp.StatusCode)
	}

	// 7. Verify gone from list
	resp, result = doAddressRequest(t, "GET", "/api/v1/addresses", auth.AccessToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET list after delete: expected 200, got %d", resp.StatusCode)
	}
	data = result["data"].([]interface{})
	if len(data) != 0 {
		t.Fatalf("expected 0 addresses after delete, got %d", len(data))
	}

	// 8. Verify gone from suggest
	resp, result = doAddressRequest(t, "GET",
		fmt.Sprintf("/api/v1/addresses/suggest?latitude=%f&longitude=%f", 28.627235, 77.364715),
		auth.AccessToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("suggest after delete: expected 200, got %d", resp.StatusCode)
	}
	suggested = result["data"].([]interface{})
	if len(suggested) != 0 {
		t.Fatalf("expected 0 suggestions after delete, got %d", len(suggested))
	}
}
