//go:build smoke

package smoke

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

var baseURL string

func TestMain(m *testing.M) {
	baseURL = strings.TrimRight(os.Getenv("SMOKE_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = "https://api.banzodelivery.com"
	}
	os.Exit(m.Run())
}

// OTP is hardcoded in the server (random generation is commented out).
const fixedOTP = "112233"

// smokePhone returns a unique Zambia (+260) mobile number for each test run.
func smokePhone() string {
	n := time.Now().UnixNano() % 100_000_000
	return fmt.Sprintf("+2609%08d", n)
}

type authResult struct {
	AccessToken  string
	RefreshToken string
	EntityID     string
	EntityType   string
}

// do sends an HTTP request and decodes the JSON response body.
func do(t *testing.T, method, path string, headers map[string]string, body interface{}) (*http.Response, map[string]interface{}) {
	t.Helper()
	var rb io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rb = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, baseURL+path, rb)
	if err != nil {
		t.Fatalf("build request %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	var result map[string]interface{}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	json.Unmarshal(b, &result)
	return resp, result
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// authenticateCustomer performs the full OTP flow for a customer.
func authenticateCustomer(t *testing.T, phone string) authResult {
	t.Helper()
	resp, _ := do(t, "POST", "/api/v1/auth/initiate-otp", nil, map[string]interface{}{
		"phone_number": phone,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initiate-otp: expected 200, got %d", resp.StatusCode)
	}
	return verifyOTP(t, phone, fixedOTP, "")
}

// verifyOTP calls verify-otp and returns parsed auth tokens.
func verifyOTP(t *testing.T, phone, otp, appType string) authResult {
	t.Helper()
	hdrs := map[string]string{}
	if appType == "de" {
		hdrs["X-App-Type"] = "de"
	}
	resp, result := do(t, "POST", "/api/v1/auth/verify-otp", hdrs, map[string]interface{}{
		"phone_number": phone,
		"otp":          otp,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify-otp: expected 200, got %d: %v", resp.StatusCode, result)
	}
	access, _ := result["access_token"].(string)
	refresh, _ := result["refresh_token"].(string)
	entityType, _ := result["entity_type"].(string)
	var entityID string
	if entity, ok := result["entity"].(map[string]interface{}); ok {
		if id, ok := entity["user_id"].(string); ok {
			entityID = id
		} else if id, ok := entity["de_id"].(string); ok {
			entityID = id
		}
	}
	return authResult{
		AccessToken:  access,
		RefreshToken: refresh,
		EntityID:     entityID,
		EntityType:   entityType,
	}
}

// registerDE creates a new DE account.
func registerDE(t *testing.T, phone string) {
	t.Helper()
	resp, result := do(t, "POST", "/api/v1/de/register", nil, map[string]interface{}{
		"phone_number": phone,
		"name":         "Smoke Test DE",
		"profile_url":  "https://example.com/photo.jpg",
		"nrc_url":      "https://example.com/nrc.jpg",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("de/register: expected 201, got %d: %v", resp.StatusCode, result)
	}
}

// authenticateDE registers (idempotent on conflict) and logs in a DE.
func authenticateDE(t *testing.T, phone string) authResult {
	t.Helper()
	resp, _ := do(t, "POST", "/api/v1/auth/initiate-otp",
		map[string]string{"X-App-Type": "de"},
		map[string]interface{}{"phone_number": phone})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initiate-otp (DE): expected 200, got %d", resp.StatusCode)
	}
	return verifyOTP(t, phone, fixedOTP, "de")
}

// ── Tests ──────────────────────────────────────────────────────────────────

func TestSmoke_Health(t *testing.T) {
	resp, _ := do(t, "GET", "/health", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestSmoke_StoreQR(t *testing.T) {
	resp, result := do(t, "GET", "/api/v1/stores/111/qr", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}
	qr, _ := result["qr_code"].(string)
	if len(qr) != 13 {
		t.Fatalf("qr_code length: expected 13, got %d (%q)", len(qr), qr)
	}
	if !strings.HasPrefix(qr, "111") {
		t.Fatalf("qr_code %q should start with store_id 111", qr)
	}
	if result["store_id"].(string) != "111" {
		t.Fatalf("store_id mismatch: got %v", result["store_id"])
	}
	if _, ok := result["valid_until"].(string); !ok {
		t.Fatal("missing valid_until field")
	}
}

func TestSmoke_CustomerAuthFlow(t *testing.T) {
	phone := smokePhone()
	auth := authenticateCustomer(t, phone)

	if auth.AccessToken == "" {
		t.Fatal("no access_token")
	}
	if auth.EntityType != "customer" {
		t.Fatalf("expected entity_type=customer, got %q", auth.EntityType)
	}
	if auth.EntityID == "" {
		t.Fatal("empty entity_id")
	}

	// Verify token works on /me
	resp, result := do(t, "GET", "/api/v1/me", bearer(auth.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/me: expected 200, got %d: %v", resp.StatusCode, result)
	}
	if result["entity_type"].(string) != "customer" {
		t.Fatalf("/me entity_type: expected customer, got %v", result["entity_type"])
	}
	if result["entity_id"].(string) != auth.EntityID {
		t.Fatalf("/me entity_id mismatch")
	}

	// Refresh
	resp, result = do(t, "POST", "/api/v1/auth/refresh", nil, map[string]interface{}{
		"refresh_token": auth.RefreshToken,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh: expected 200, got %d: %v", resp.StatusCode, result)
	}
	newToken, _ := result["access_token"].(string)
	if newToken == "" {
		t.Fatal("refresh did not return access_token")
	}

	// Refreshed token should still work
	resp, _ = do(t, "GET", "/api/v1/me", bearer(newToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refreshed token rejected by /me (status %d)", resp.StatusCode)
	}
}

func TestSmoke_AddressLifecycle(t *testing.T) {
	phone := smokePhone()
	auth := authenticateCustomer(t, phone)

	// Create
	resp, result := do(t, "POST", "/api/v1/addresses", bearer(auth.AccessToken), map[string]interface{}{
		"receiver_name":      "Smoke Test",
		"receiver_phone":     "+919876543210",
		"building_and_floor": "Floor 1",
		"address_line_1":     "MG Road",
		"latitude":           12.975,
		"longitude":          77.640,
		"tag_key":            "home",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create address: expected 201, got %d: %v", resp.StatusCode, result)
	}
	data := result["data"].(map[string]interface{})
	addrID, _ := data["address_id"].(string)
	if addrID == "" {
		t.Fatal("missing address_id in create response")
	}

	// Get by ID
	resp, result = do(t, "GET", "/api/v1/addresses/"+addrID, bearer(auth.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get by ID: expected 200, got %d: %v", resp.StatusCode, result)
	}
	if result["data"].(map[string]interface{})["address_id"].(string) != addrID {
		t.Fatal("address_id mismatch on get")
	}

	// List
	resp, result = do(t, "GET", "/api/v1/addresses", bearer(auth.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: expected 200, got %d: %v", resp.StatusCode, result)
	}
	items := result["data"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("expected 1 address in list, got %d", len(items))
	}

	// Patch
	resp, result = do(t, "PATCH", "/api/v1/addresses/"+addrID, bearer(auth.AccessToken), map[string]interface{}{
		"receiver_name": "Updated Smoke",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch: expected 200, got %d: %v", resp.StatusCode, result)
	}
	if result["data"].(map[string]interface{})["receiver_name"].(string) != "Updated Smoke" {
		t.Fatal("receiver_name not updated after patch")
	}

	// Suggest (same coords)
	resp, result = do(t, "GET",
		"/api/v1/addresses/suggest?latitude=12.975&longitude=77.640",
		bearer(auth.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("suggest: expected 200, got %d: %v", resp.StatusCode, result)
	}
	suggested := result["data"].([]interface{})
	if len(suggested) != 1 {
		t.Fatalf("suggest: expected 1 result, got %d", len(suggested))
	}

	// Delete
	resp, result = do(t, "DELETE", "/api/v1/addresses/"+addrID, bearer(auth.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d: %v", resp.StatusCode, result)
	}

	// Verify gone
	resp, _ = do(t, "GET", "/api/v1/addresses/"+addrID, bearer(auth.AccessToken), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after delete: expected 404, got %d", resp.StatusCode)
	}
}

func TestSmoke_Serviceability(t *testing.T) {
	phone := smokePhone()
	auth := authenticateCustomer(t, phone)

	resp, result := do(t, "POST", "/api/v1/serviceability", bearer(auth.AccessToken), map[string]interface{}{
		"latitude":  12.975,
		"longitude": 77.640,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}
	data, ok := result["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing data field in response: %v", result)
	}
	if _, ok := data["serviceable"]; !ok {
		t.Fatal("missing serviceable field in data")
	}
	t.Logf("serviceable=%v", data["serviceable"])
}

func TestSmoke_DEFlow(t *testing.T) {
	phone := smokePhone()
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)

	if tokens.AccessToken == "" {
		t.Fatal("no access_token for DE")
	}
	if tokens.EntityType != "de" {
		t.Fatalf("expected entity_type=de, got %q", tokens.EntityType)
	}
	if tokens.EntityID == "" {
		t.Fatal("empty entity_id for DE")
	}

	// GET /de/me
	resp, result := do(t, "GET", "/api/v1/de/me", bearer(tokens.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/de/me: expected 200, got %d: %v", resp.StatusCode, result)
	}
	if result["status"].(string) != "offline" {
		t.Fatalf("/de/me: expected status=offline, got %v", result["status"])
	}
	if result["phone_number"].(string) != phone {
		t.Fatalf("/de/me: phone_number mismatch")
	}

	// Refresh preserves entity_type
	resp, result = do(t, "POST", "/api/v1/auth/refresh", nil, map[string]interface{}{
		"refresh_token": tokens.RefreshToken,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DE refresh: expected 200, got %d: %v", resp.StatusCode, result)
	}
	newToken, _ := result["access_token"].(string)
	resp, _ = do(t, "GET", "/api/v1/de/me", bearer(newToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refreshed DE token rejected by /de/me (status %d)", resp.StatusCode)
	}

	// Duty start using live QR code
	_, qrResult := do(t, "GET", "/api/v1/stores/111/qr", nil, nil)
	qrCode, _ := qrResult["qr_code"].(string)
	if qrCode == "" {
		t.Fatal("could not get QR code for duty start")
	}
	resp, result = do(t, "POST", "/api/v1/de/duty/start", bearer(tokens.AccessToken), map[string]interface{}{
		"qr_code": qrCode,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("duty/start: expected 200, got %d: %v", resp.StatusCode, result)
	}
	if result["status"].(string) != "eligible" {
		t.Fatalf("duty/start: expected status=eligible, got %v", result["status"])
	}
	if result["store_id"].(string) != "111" {
		t.Fatalf("duty/start: expected store_id=111, got %v", result["store_id"])
	}
}

func TestSmoke_AuthErrors(t *testing.T) {
	// No token → 401
	resp, _ := do(t, "GET", "/api/v1/me", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no auth: expected 401, got %d", resp.StatusCode)
	}

	// Invalid token → 401
	resp, _ = do(t, "GET", "/api/v1/me", bearer("not.a.valid.jwt"), nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: expected 401, got %d", resp.StatusCode)
	}

	// Customer token on DE endpoint → 403
	phone := smokePhone()
	auth := authenticateCustomer(t, phone)
	resp, _ = do(t, "GET", "/api/v1/de/me", bearer(auth.AccessToken), nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("customer on /de/me: expected 403, got %d", resp.StatusCode)
	}
}
