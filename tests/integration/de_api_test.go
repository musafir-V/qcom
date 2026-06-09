//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamodbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// ── helpers ──────────────────────────────────────────────────────────────────

const (
	testStoreID = "111"
	testDEPhone = "+26097000001"
)

func doRequest(t *testing.T, method, path string, headers map[string]string, body interface{}) (*http.Response, map[string]interface{}) {
	t.Helper()

	var reqBody io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reqBody = bytes.NewReader(b)
	}

	req, _ := http.NewRequest(method, testServer.URL+path, reqBody)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}

	var result map[string]interface{}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	json.Unmarshal(b, &result)
	return resp, result
}

func bearerHeaders(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func deHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + token,
		"X-App-Type":    "de",
	}
}

// registerDE creates a new DE and returns the de_id. Unique phone per test.
func registerDE(t *testing.T, phone string) string {
	t.Helper()
	resp, result := doRequest(t, "POST", "/api/v1/de/register", nil, map[string]interface{}{
		"phone_number": phone,
		"name":         "Test DE",
		"profile_url":  "https://example.com/photo.jpg",
		"nrc_url":      "https://example.com/nrc.jpg",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("registerDE(%s): expected 201, got %d: %v", phone, resp.StatusCode, result)
	}
	return result["de_id"].(string)
}

// authenticateDE registers (if needed) and logs in a DE, returning tokens.
func authenticateDE(t *testing.T, phone string) authTokens {
	t.Helper()

	// Initiate OTP with DE header
	req, _ := http.NewRequest("POST", testServer.URL+"/api/v1/auth/initiate-otp",
		strings.NewReader(fmt.Sprintf(`{"phone_number":"%s"}`, phone)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-App-Type", "de")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("initiate-otp (DE) failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initiate-otp (DE) returned %d", resp.StatusCode)
	}

	otp, err := getTestOTP(phone)
	if err != nil {
		t.Fatalf("getTestOTP(%s): %v", phone, err)
	}

	return doVerifyOTP(t, phone, otp, "de")
}

// currentQRCode fetches the QR code for a store from the API.
func currentQRCode(t *testing.T, storeID string) string {
	t.Helper()
	resp, result := doRequest(t, "GET", "/api/v1/stores/"+storeID+"/qr", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /stores/%s/qr returned %d", storeID, resp.StatusCode)
	}
	return result["qr_code"].(string)
}

// setDEStatus directly writes a DE status to DynamoDB, bypassing business logic.
// Used to put a DE into states (e.g. free) that are only reached via the cron
// (which is not yet implemented).
func setDEStatus(t *testing.T, phone, status, storeID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)

	expr := "SET #status = :status, updated_at = :now REMOVE duty_index_key, current_store_id, current_order_id"
	names := map[string]string{"#status": "status"}
	vals := map[string]dynamodbtypes.AttributeValue{
		":status": &dynamodbtypes.AttributeValueMemberS{Value: status},
		":now":    &dynamodbtypes.AttributeValueMemberS{Value: now},
	}

	if storeID != "" {
		expr = "SET #status = :status, updated_at = :now, current_store_id = :store"
		vals[":store"] = &dynamodbtypes.AttributeValueMemberS{Value: storeID}
	}

	_, err := dynamoClient.UpdateItem(context.Background(), &dynamodb.UpdateItemInput{
		TableName: aws.String(testTableName),
		Key: map[string]dynamodbtypes.AttributeValue{
			"PK": &dynamodbtypes.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &dynamodbtypes.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression:          aws.String(expr),
		ExpressionAttributeNames:  names,
		ExpressionAttributeValues: vals,
	})
	if err != nil {
		t.Fatalf("setDEStatus(%s, %s): %v", phone, status, err)
	}
}

// assertDEStatus fetches the DE from DynamoDB and asserts its status field.
func assertDEStatus(t *testing.T, phone, wantStatus string) {
	t.Helper()
	result, err := dynamoClient.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: aws.String(testTableName),
		Key: map[string]dynamodbtypes.AttributeValue{
			"PK": &dynamodbtypes.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &dynamodbtypes.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil || result.Item == nil {
		t.Fatalf("assertDEStatus: DE not found for %s", phone)
	}
	got := result.Item["status"].(*dynamodbtypes.AttributeValueMemberS).Value
	if got != wantStatus {
		t.Errorf("DE %s: expected status %q, got %q", phone, wantStatus, got)
	}
}

// uniquePhone generates a phone number that is unique per test to avoid
// cross-test state pollution. The suffix is the last 6 digits of UnixNano.
func uniquePhone(seed string) string {
	n := time.Now().UnixNano() % 1_000_000
	return fmt.Sprintf("+2609%s%06d", seed, n)
}

// ── B: DE Registration ────────────────────────────────────────────────────────

func TestDERegister_Success(t *testing.T) {
	phone := uniquePhone("10")
	resp, result := doRequest(t, "POST", "/api/v1/de/register", nil, map[string]interface{}{
		"phone_number": phone,
		"name":         "John Banda",
		"profile_url":  "https://s3.example.com/photo.jpg",
		"nrc_url":      "https://s3.example.com/nrc.jpg",
	})

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %v", resp.StatusCode, result)
	}
	if result["de_id"] == nil || result["de_id"].(string) == "" {
		t.Fatal("response missing de_id")
	}
	if result["status"].(string) != "offline" {
		t.Fatalf("expected status=offline, got %v", result["status"])
	}
	if result["phone_number"].(string) != phone {
		t.Fatalf("phone_number mismatch: got %v", result["phone_number"])
	}
}

func TestDERegister_DuplicatePhone(t *testing.T) {
	phone := uniquePhone("20")
	registerDE(t, phone) // first registration

	resp, result := doRequest(t, "POST", "/api/v1/de/register", nil, map[string]interface{}{
		"phone_number": phone,
		"name":         "Duplicate",
		"profile_url":  "https://example.com/p.jpg",
		"nrc_url":      "https://example.com/n.jpg",
	})

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %v", resp.StatusCode, result)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"] != "DE_ALREADY_EXISTS" {
		t.Fatalf("expected DE_ALREADY_EXISTS, got %v", errObj["code"])
	}
}

func TestDERegister_MissingName(t *testing.T) {
	resp, result := doRequest(t, "POST", "/api/v1/de/register", nil, map[string]interface{}{
		"phone_number": uniquePhone("30"),
		"profile_url":  "https://example.com/p.jpg",
		"nrc_url":      "https://example.com/n.jpg",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}
}

func TestDERegister_MissingProfileURL(t *testing.T) {
	resp, result := doRequest(t, "POST", "/api/v1/de/register", nil, map[string]interface{}{
		"phone_number": uniquePhone("31"),
		"name":         "Test DE",
		"nrc_url":      "https://example.com/n.jpg",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}
}

func TestDERegister_MissingNRCURL(t *testing.T) {
	resp, result := doRequest(t, "POST", "/api/v1/de/register", nil, map[string]interface{}{
		"phone_number": uniquePhone("32"),
		"name":         "Test DE",
		"profile_url":  "https://example.com/p.jpg",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}
}

func TestDERegister_InvalidPhone(t *testing.T) {
	resp, result := doRequest(t, "POST", "/api/v1/de/register", nil, map[string]interface{}{
		"phone_number": "not-a-phone",
		"name":         "Test DE",
		"profile_url":  "https://example.com/p.jpg",
		"nrc_url":      "https://example.com/n.jpg",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}
}

// ── B: DE OTP Auth ────────────────────────────────────────────────────────────

func TestDEInitiateOTP_Success(t *testing.T) {
	phone := uniquePhone("40")
	registerDE(t, phone)

	req, _ := http.NewRequest("POST", testServer.URL+"/api/v1/auth/initiate-otp",
		strings.NewReader(fmt.Sprintf(`{"phone_number":"%s"}`, phone)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-App-Type", "de")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(b))
	}
}

func TestDEInitiateOTP_PhoneNotRegistered(t *testing.T) {
	req, _ := http.NewRequest("POST", testServer.URL+"/api/v1/auth/initiate-otp",
		strings.NewReader(fmt.Sprintf(`{"phone_number":"%s"}`, uniquePhone("41"))))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-App-Type", "de")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 404, got %d: %s", resp.StatusCode, string(b))
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
}

func TestDEVerifyOTP_Success(t *testing.T) {
	phone := uniquePhone("42")
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)

	if tokens.AccessToken == "" {
		t.Fatal("expected access token")
	}
	if tokens.EntityType != "de" {
		t.Fatalf("expected entity_type=de, got %q", tokens.EntityType)
	}
	if tokens.EntityID == "" {
		t.Fatal("expected non-empty entity_id (de_id)")
	}
}

func TestDEVerifyOTP_WrongOTP(t *testing.T) {
	phone := uniquePhone("43")
	registerDE(t, phone)

	// Initiate OTP so the DE is in valid state
	req, _ := http.NewRequest("POST", testServer.URL+"/api/v1/auth/initiate-otp",
		strings.NewReader(fmt.Sprintf(`{"phone_number":"%s"}`, phone)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-App-Type", "de")
	http.DefaultClient.Do(req)

	resp, _ := doRequest(t, "POST", "/api/v1/auth/verify-otp",
		map[string]string{"X-App-Type": "de"},
		map[string]interface{}{"phone_number": phone, "otp": "000000"})

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

// ── B: Customer token shape ───────────────────────────────────────────────────

func TestCustomerVerifyOTP_EntityType(t *testing.T) {
	phone := uniquePhone("50")
	tokens := authenticateUser(t, phone)

	if tokens.EntityType != "customer" {
		t.Fatalf("expected entity_type=customer, got %q", tokens.EntityType)
	}
	if tokens.EntityID == "" {
		t.Fatal("expected non-empty entity_id")
	}
}

func TestCustomerToken_HasEntityID(t *testing.T) {
	phone := uniquePhone("51")
	tokens := authenticateUser(t, phone)

	// Call /me and verify response uses entity_id
	resp, result := doRequest(t, "GET", "/api/v1/me", bearerHeaders(tokens.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}
	if result["entity_id"].(string) != tokens.EntityID {
		t.Fatalf("entity_id mismatch: got %v, want %s", result["entity_id"], tokens.EntityID)
	}
	if result["entity_type"].(string) != "customer" {
		t.Fatalf("entity_type mismatch: got %v", result["entity_type"])
	}
}

// ── B: QR Code API ───────────────────────────────────────────────────────────

func TestGetStoreQR_Format(t *testing.T) {
	resp, result := doRequest(t, "GET", "/api/v1/stores/"+testStoreID+"/qr", nil, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}

	qrCode, ok := result["qr_code"].(string)
	if !ok || qrCode == "" {
		t.Fatal("missing or empty qr_code field")
	}
	if len(qrCode) != 13 {
		t.Fatalf("expected qr_code length 13, got %d (%q)", len(qrCode), qrCode)
	}
	if !strings.HasPrefix(qrCode, testStoreID) {
		t.Fatalf("qr_code %q does not start with storeId %q", qrCode, testStoreID)
	}
	if result["store_id"].(string) != testStoreID {
		t.Fatalf("store_id mismatch: got %v", result["store_id"])
	}
	if _, ok := result["valid_until"].(string); !ok {
		t.Fatal("missing valid_until field")
	}
}

func TestGetStoreQR_ValidUntil(t *testing.T) {
	_, result := doRequest(t, "GET", "/api/v1/stores/"+testStoreID+"/qr", nil, nil)

	raw, _ := result["valid_until"].(string)
	until, err := time.Parse("2006-01-02T15:04:05Z07:00", raw)
	if err != nil {
		t.Fatalf("could not parse valid_until %q: %v", raw, err)
	}
	if !until.After(time.Now()) {
		t.Errorf("valid_until %v should be in the future", until)
	}
}

func TestGetStoreQR_NoAuthRequired(t *testing.T) {
	// No Authorization header — should still return 200
	resp, _ := doRequest(t, "GET", "/api/v1/stores/"+testStoreID+"/qr", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 without auth, got %d", resp.StatusCode)
	}
}

// ── B: DE Duty Start ─────────────────────────────────────────────────────────

func TestDEStartDuty_Success(t *testing.T) {
	phone := uniquePhone("60")
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)

	qrCode := currentQRCode(t, testStoreID)
	resp, result := doRequest(t, "POST", "/api/v1/de/duty/start",
		bearerHeaders(tokens.AccessToken),
		map[string]interface{}{"qr_code": qrCode})

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}
	if result["status"].(string) != "eligible" {
		t.Fatalf("expected status=eligible, got %v", result["status"])
	}
	if result["store_id"].(string) != testStoreID {
		t.Fatalf("expected store_id=%s, got %v", testStoreID, result["store_id"])
	}

	assertDEStatus(t, phone, "eligible")
}

func TestDEStartDuty_ValidQRFromFreeState(t *testing.T) {
	phone := uniquePhone("61")
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)

	// Simulate DE returning from a delivery: set status directly to "free"
	setDEStatus(t, phone, "free", "")

	qrCode := currentQRCode(t, testStoreID)
	resp, result := doRequest(t, "POST", "/api/v1/de/duty/start",
		bearerHeaders(tokens.AccessToken),
		map[string]interface{}{"qr_code": qrCode})

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("free→eligible: expected 200, got %d: %v", resp.StatusCode, result)
	}
	assertDEStatus(t, phone, "eligible")
}

func TestDEStartDuty_ExpiredQR(t *testing.T) {
	phone := uniquePhone("62")
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)

	// Past timestamp — always expired
	expiredQR := "1112020010100"
	resp, result := doRequest(t, "POST", "/api/v1/de/duty/start",
		bearerHeaders(tokens.AccessToken),
		map[string]interface{}{"qr_code": expiredQR})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for expired QR, got %d: %v", resp.StatusCode, result)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"] != "QR_EXPIRED" {
		t.Fatalf("expected QR_EXPIRED, got %v", errObj["code"])
	}
}

func TestDEStartDuty_MalformedQR(t *testing.T) {
	phone := uniquePhone("63")
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)

	resp, result := doRequest(t, "POST", "/api/v1/de/duty/start",
		bearerHeaders(tokens.AccessToken),
		map[string]interface{}{"qr_code": "short"})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed QR, got %d: %v", resp.StatusCode, result)
	}
}

func TestDEStartDuty_AlreadyEligible(t *testing.T) {
	phone := uniquePhone("64")
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)

	qrCode := currentQRCode(t, testStoreID)

	// First scan succeeds
	doRequest(t, "POST", "/api/v1/de/duty/start",
		bearerHeaders(tokens.AccessToken),
		map[string]interface{}{"qr_code": qrCode})

	// Second scan while already eligible should fail
	resp, result := doRequest(t, "POST", "/api/v1/de/duty/start",
		bearerHeaders(tokens.AccessToken),
		map[string]interface{}{"qr_code": qrCode})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when already eligible, got %d: %v", resp.StatusCode, result)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"] != "INVALID_STATE" {
		t.Fatalf("expected INVALID_STATE, got %v", errObj["code"])
	}
}

func TestDEStartDuty_BusyDE(t *testing.T) {
	phone := uniquePhone("65")
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)

	// Simulate being on an active delivery
	setDEStatus(t, phone, "busy", testStoreID)

	qrCode := currentQRCode(t, testStoreID)
	resp, result := doRequest(t, "POST", "/api/v1/de/duty/start",
		bearerHeaders(tokens.AccessToken),
		map[string]interface{}{"qr_code": qrCode})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for busy DE, got %d: %v", resp.StatusCode, result)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"] != "INVALID_STATE" {
		t.Fatalf("expected INVALID_STATE, got %v", errObj["code"])
	}
}

func TestDEStartDuty_NoToken(t *testing.T) {
	resp, _ := doRequest(t, "POST", "/api/v1/de/duty/start", nil,
		map[string]interface{}{"qr_code": "1112026010100"})

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestDEStartDuty_CustomerTokenRejected(t *testing.T) {
	phone := uniquePhone("66")
	customerTokens := authenticateUser(t, phone)

	qrCode := currentQRCode(t, testStoreID)
	resp, _ := doRequest(t, "POST", "/api/v1/de/duty/start",
		bearerHeaders(customerTokens.AccessToken),
		map[string]interface{}{"qr_code": qrCode})

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for customer token on DE endpoint, got %d", resp.StatusCode)
	}
}

// ── B: DE Me ─────────────────────────────────────────────────────────────────

func TestDEMe_Success(t *testing.T) {
	phone := uniquePhone("70")
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)

	resp, result := doRequest(t, "GET", "/api/v1/de/me", bearerHeaders(tokens.AccessToken), nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}
	if result["de_id"] == nil || result["de_id"].(string) == "" {
		t.Fatal("missing de_id in response")
	}
	if result["phone_number"].(string) != phone {
		t.Fatalf("phone_number mismatch: got %v", result["phone_number"])
	}
	if result["status"].(string) != "offline" {
		t.Fatalf("expected status=offline for fresh DE, got %v", result["status"])
	}
}

func TestDEMe_StatusReflectsAfterDutyStart(t *testing.T) {
	phone := uniquePhone("71")
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)

	// Start duty
	qrCode := currentQRCode(t, testStoreID)
	doRequest(t, "POST", "/api/v1/de/duty/start",
		bearerHeaders(tokens.AccessToken),
		map[string]interface{}{"qr_code": qrCode})

	// Check /de/me reflects updated status
	resp, result := doRequest(t, "GET", "/api/v1/de/me", bearerHeaders(tokens.AccessToken), nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if result["status"].(string) != "eligible" {
		t.Fatalf("expected status=eligible after duty start, got %v", result["status"])
	}
	if result["current_store_id"].(string) != testStoreID {
		t.Fatalf("expected current_store_id=%s, got %v", testStoreID, result["current_store_id"])
	}
}

func TestDEMe_NoToken(t *testing.T) {
	resp, _ := doRequest(t, "GET", "/api/v1/de/me", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestDEMe_CustomerTokenRejected(t *testing.T) {
	phone := uniquePhone("72")
	tokens := authenticateUser(t, phone)

	resp, _ := doRequest(t, "GET", "/api/v1/de/me", bearerHeaders(tokens.AccessToken), nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

// ── B: Refresh Token preserves entity_type ───────────────────────────────────

func TestRefresh_DETokenPreservesEntityType(t *testing.T) {
	phone := uniquePhone("80")
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)

	resp, result := doRequest(t, "POST", "/api/v1/auth/refresh", nil,
		map[string]interface{}{"refresh_token": tokens.RefreshToken})

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}

	newAccess, _ := result["access_token"].(string)
	if newAccess == "" {
		t.Fatal("expected new access_token")
	}

	// The new token should still grant DE access
	meResp, _ := doRequest(t, "GET", "/api/v1/de/me", bearerHeaders(newAccess), nil)
	if meResp.StatusCode != http.StatusOK {
		t.Fatalf("refreshed DE token rejected by /de/me (status %d)", meResp.StatusCode)
	}
}

func TestRefresh_CustomerTokenPreservesEntityType(t *testing.T) {
	phone := uniquePhone("81")
	tokens := authenticateUser(t, phone)

	resp, result := doRequest(t, "POST", "/api/v1/auth/refresh", nil,
		map[string]interface{}{"refresh_token": tokens.RefreshToken})

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}

	newAccess, _ := result["access_token"].(string)

	// New token should still work for customer endpoints
	meResp, meResult := doRequest(t, "GET", "/api/v1/me", bearerHeaders(newAccess), nil)
	if meResp.StatusCode != http.StatusOK {
		t.Fatalf("refreshed customer token rejected by /me (status %d)", meResp.StatusCode)
	}
	if meResult["entity_type"].(string) != "customer" {
		t.Fatalf("expected entity_type=customer after refresh, got %v", meResult["entity_type"])
	}

	// And should still be blocked from DE endpoints
	deResp, _ := doRequest(t, "GET", "/api/v1/de/me", bearerHeaders(newAccess), nil)
	if deResp.StatusCode != http.StatusForbidden {
		t.Fatalf("refreshed customer token should be blocked by /de/me, got %d", deResp.StatusCode)
	}
}

func TestGetMe_HomeStatsFields(t *testing.T) {
	phone := uniquePhone("90")
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)

	resp, result := doRequest(t, "GET", "/api/v1/de/me",
		bearerHeaders(tokens.AccessToken), nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}

	tt, ok := result["trips_today"].(float64)
	if !ok {
		t.Fatalf("missing/!number trips_today: %v", result["trips_today"])
	}
	if tt != 0 {
		t.Fatalf("fresh DE: expected trips_today=0, got %v", tt)
	}

	te, ok := result["today_earnings_zmw"].(float64)
	if !ok {
		t.Fatalf("missing/!number today_earnings_zmw: %v", result["today_earnings_zmw"])
	}
	if te != 0 {
		t.Fatalf("fresh DE: expected today_earnings_zmw=0, got %v", te)
	}
}
