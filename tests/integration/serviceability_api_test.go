//go:build integration

package integration

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamodbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/models"
)

// defaultGeocodeResult is what the mock geocoder returns unless a test overrides it.
const defaultGeocodeResult = "Indiranagar, Bengaluru"

// mockGeocoder is an in-memory service.Geocoder so integration tests never make a
// real Google Maps call. Tests swap fn to control success/failure behaviour.
type mockGeocoder struct {
	fn func(ctx context.Context, lat, lng float64) (string, error)
}

func (m *mockGeocoder) ReverseGeocode(ctx context.Context, lat, lng float64) (string, error) {
	if m.fn != nil {
		return m.fn(ctx, lat, lng)
	}
	return defaultGeocodeResult, nil
}

// testGeocoder is wired into the test server by setupServer (see upload_api_test.go).
var testGeocoder = &mockGeocoder{}

// seedTestDarkstores writes two non-overlapping darkstore polygons to the test
// table. Idempotent — safe to call from every test.
func seedTestDarkstores(t *testing.T) {
	t.Helper()

	darkstores := []models.Darkstore{
		{
			DarkstoreID: "DS-TEST-1",
			Name:        "Test Darkstore 1 (Indiranagar)",
			Latitude:    12.975,
			Longitude:   77.640,
			IsActive:    true,
			CreatedAt:   "2026-01-01T00:00:00Z",
			UpdatedAt:   "2026-01-01T00:00:00Z",
			Polygon: []models.PolygonPoint{
				{Lat: 12.96, Lng: 77.62},
				{Lat: 12.96, Lng: 77.66},
				{Lat: 12.99, Lng: 77.66},
				{Lat: 12.99, Lng: 77.62},
			},
		},
		{
			DarkstoreID: "DS-TEST-2",
			Name:        "Test Darkstore 2 (Koramangala)",
			Latitude:    12.93,
			Longitude:   77.62,
			IsActive:    true,
			CreatedAt:   "2026-01-01T00:00:00Z",
			UpdatedAt:   "2026-01-01T00:00:00Z",
			Polygon: []models.PolygonPoint{
				{Lat: 12.91, Lng: 77.60},
				{Lat: 12.91, Lng: 77.64},
				{Lat: 12.95, Lng: 77.64},
				{Lat: 12.95, Lng: 77.60},
			},
		},
	}

	for _, ds := range darkstores {
		item, err := attributevalue.MarshalMap(ds)
		if err != nil {
			t.Fatalf("failed to marshal darkstore %s: %v", ds.DarkstoreID, err)
		}
		item["PK"] = &dynamodbtypes.AttributeValueMemberS{Value: ds.GetPK()}
		item["SK"] = &dynamodbtypes.AttributeValueMemberS{Value: ds.GetSK()}

		if _, err := dynamoClient.PutItem(context.TODO(), &dynamodb.PutItemInput{
			TableName: aws.String(testTableName),
			Item:      item,
		}); err != nil {
			t.Fatalf("failed to seed darkstore %s: %v", ds.DarkstoreID, err)
		}
	}
}

func doServiceabilityRequest(t *testing.T, token string, body interface{}) (*http.Response, map[string]interface{}) {
	t.Helper()
	return doAddressRequest(t, "POST", "/api/v1/serviceability", token, body)
}

// ── validation / auth ────────────────────────────────────────────────────

func TestServiceability_NoAuth(t *testing.T) {
	resp, _ := doServiceabilityRequest(t, "", map[string]interface{}{
		"latitude": 12.975, "longitude": 77.640,
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestServiceability_MissingLatitude(t *testing.T) {
	auth := authenticateUser(t, "+13000000001")

	resp, result := doServiceabilityRequest(t, auth.AccessToken, map[string]interface{}{
		"longitude": 77.640,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"] != "MISSING_FIELD" {
		t.Fatalf("expected MISSING_FIELD, got %v", errObj["code"])
	}
}

func TestServiceability_MissingLongitude(t *testing.T) {
	auth := authenticateUser(t, "+13000000002")

	resp, result := doServiceabilityRequest(t, auth.AccessToken, map[string]interface{}{
		"latitude": 12.975,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"] != "MISSING_FIELD" {
		t.Fatalf("expected MISSING_FIELD, got %v", errObj["code"])
	}
}

func TestServiceability_InvalidLatitude(t *testing.T) {
	auth := authenticateUser(t, "+13000000003")

	resp, result := doServiceabilityRequest(t, auth.AccessToken, map[string]interface{}{
		"latitude": 200.0, "longitude": 77.640,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"] != "INVALID_COORDINATES" {
		t.Fatalf("expected INVALID_COORDINATES, got %v", errObj["code"])
	}
}

func TestServiceability_InvalidLongitude(t *testing.T) {
	auth := authenticateUser(t, "+13000000004")

	resp, result := doServiceabilityRequest(t, auth.AccessToken, map[string]interface{}{
		"latitude": 12.975, "longitude": 200.0,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"] != "INVALID_COORDINATES" {
		t.Fatalf("expected INVALID_COORDINATES, got %v", errObj["code"])
	}
}

func TestServiceability_InvalidJSON(t *testing.T) {
	auth := authenticateUser(t, "+13000000005")

	req, _ := http.NewRequest("POST", testServer.URL+"/api/v1/serviceability", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

// ── point-in-polygon serviceability ──────────────────────────────────────

func TestServiceability_Unserviceable(t *testing.T) {
	seedTestDarkstores(t)
	auth := authenticateUser(t, "+13000000010")

	resp, result := doServiceabilityRequest(t, auth.AccessToken, map[string]interface{}{
		"latitude": 13.5, "longitude": 77.6,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}

	data := result["data"].(map[string]interface{})
	if data["serviceable"].(bool) != false {
		t.Fatalf("expected serviceable=false, got %v", data["serviceable"])
	}
	if _, present := data["resolved_address"]; present {
		t.Fatal("unserviceable response should have no resolved_address")
	}
}

func TestServiceability_ServiceableGeocoded(t *testing.T) {
	seedTestDarkstores(t)
	auth := authenticateUser(t, "+13000000011")

	// User has no saved addresses -> resolution falls back to the geocoder.
	resp, result := doServiceabilityRequest(t, auth.AccessToken, map[string]interface{}{
		"latitude": 12.975, "longitude": 77.640,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}

	data := result["data"].(map[string]interface{})
	if data["serviceable"].(bool) != true {
		t.Fatal("expected serviceable=true")
	}
	if data["darkstore_id"].(string) != "DS-TEST-1" {
		t.Fatalf("expected darkstore_id DS-TEST-1, got %v", data["darkstore_id"])
	}

	ra := data["resolved_address"].(map[string]interface{})
	if ra["source"].(string) != "geocoded" {
		t.Fatalf("expected source geocoded, got %v", ra["source"])
	}
	if ra["address_line"].(string) != defaultGeocodeResult {
		t.Fatalf("expected address_line %q, got %v", defaultGeocodeResult, ra["address_line"])
	}
	if ra["tag"] != nil {
		t.Fatalf("expected tag null for geocoded result, got %v", ra["tag"])
	}
	if ra["address_id"] != nil {
		t.Fatalf("expected address_id null for geocoded result, got %v", ra["address_id"])
	}
}

func TestServiceability_SecondDarkstore(t *testing.T) {
	seedTestDarkstores(t)
	auth := authenticateUser(t, "+13000000012")

	resp, result := doServiceabilityRequest(t, auth.AccessToken, map[string]interface{}{
		"latitude": 12.93, "longitude": 77.62,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}

	data := result["data"].(map[string]interface{})
	if data["serviceable"].(bool) != true {
		t.Fatal("expected serviceable=true")
	}
	if data["darkstore_id"].(string) != "DS-TEST-2" {
		t.Fatalf("expected darkstore_id DS-TEST-2, got %v", data["darkstore_id"])
	}
}

// ── saved-address resolution ─────────────────────────────────────────────

func TestServiceability_ServiceableSavedAddress(t *testing.T) {
	seedTestDarkstores(t)
	auth := authenticateUser(t, "+13000000020")

	_, addrID := createTestAddress(t, auth.AccessToken, map[string]interface{}{
		"latitude":       12.975,
		"longitude":      77.640,
		"address_line_2": "Near Test Park",
		"label":          "home",
	})

	resp, result := doServiceabilityRequest(t, auth.AccessToken, map[string]interface{}{
		"latitude": 12.975, "longitude": 77.640,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}

	data := result["data"].(map[string]interface{})
	if data["darkstore_id"].(string) != "DS-TEST-1" {
		t.Fatalf("expected darkstore_id DS-TEST-1, got %v", data["darkstore_id"])
	}

	ra := data["resolved_address"].(map[string]interface{})
	if ra["source"].(string) != "saved_address" {
		t.Fatalf("expected source saved_address, got %v", ra["source"])
	}
	if ra["address_id"].(string) != addrID {
		t.Fatalf("expected address_id %s, got %v", addrID, ra["address_id"])
	}
	if ra["tag"].(string) != "home" {
		t.Fatalf("expected tag home, got %v", ra["tag"])
	}
	if ra["address_line"].(string) != "Near Test Park" {
		t.Fatalf("expected address_line from address_line_2, got %v", ra["address_line"])
	}
}

func TestServiceability_SavedAddressTooFarFallsBackToGeocode(t *testing.T) {
	seedTestDarkstores(t)
	auth := authenticateUser(t, "+13000000021")

	// Saved address is inside DS-TEST-1 but ~1 km from the query point.
	createTestAddress(t, auth.AccessToken, map[string]interface{}{
		"latitude":  12.975,
		"longitude": 77.640,
	})

	resp, result := doServiceabilityRequest(t, auth.AccessToken, map[string]interface{}{
		"latitude": 12.985, "longitude": 77.650,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}

	ra := result["data"].(map[string]interface{})["resolved_address"].(map[string]interface{})
	if ra["source"].(string) != "geocoded" {
		t.Fatalf("address >50m away should fall back to geocoded, got %v", ra["source"])
	}
}

func TestServiceability_PicksNearestSavedAddress(t *testing.T) {
	seedTestDarkstores(t)
	auth := authenticateUser(t, "+13000000022")

	// Nearest: exactly on the query point.
	_, nearID := createTestAddress(t, auth.AccessToken, map[string]interface{}{
		"building_and_floor": "Nearest Building",
		"latitude":           12.975,
		"longitude":          77.640,
		"address_line_2":     "Nearest",
	})
	// Farther: ~33 m away, still within the 50 m radius.
	createTestAddress(t, auth.AccessToken, map[string]interface{}{
		"building_and_floor": "Farther Building",
		"latitude":           12.9753,
		"longitude":          77.640,
		"address_line_2":     "Farther",
	})

	resp, result := doServiceabilityRequest(t, auth.AccessToken, map[string]interface{}{
		"latitude": 12.975, "longitude": 77.640,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}

	ra := result["data"].(map[string]interface{})["resolved_address"].(map[string]interface{})
	if ra["address_id"].(string) != nearID {
		t.Fatalf("expected nearest address %s, got %v", nearID, ra["address_id"])
	}
	if ra["address_line"].(string) != "Nearest" {
		t.Fatalf("expected address_line Nearest, got %v", ra["address_line"])
	}
}

func TestServiceability_OtherUsersAddressIgnored(t *testing.T) {
	seedTestDarkstores(t)
	userA := authenticateUser(t, "+13000000023")
	userB := authenticateUser(t, "+13000000024")

	// User A saves an address exactly on the shared query point.
	createTestAddress(t, userA.AccessToken, map[string]interface{}{
		"latitude":       12.975,
		"longitude":      77.640,
		"address_line_2": "User A home",
	})

	// User B queries the same point — must not match user A's address.
	resp, result := doServiceabilityRequest(t, userB.AccessToken, map[string]interface{}{
		"latitude": 12.975, "longitude": 77.640,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}

	ra := result["data"].(map[string]interface{})["resolved_address"].(map[string]interface{})
	if ra["source"].(string) != "geocoded" {
		t.Fatalf("user B must not match user A's address; expected geocoded, got %v", ra["source"])
	}
}

// ── geocoder failure handling ────────────────────────────────────────────

func TestServiceability_GeocodeFailureIsGraceful(t *testing.T) {
	seedTestDarkstores(t)
	auth := authenticateUser(t, "+13000000030")

	testGeocoder.fn = func(ctx context.Context, lat, lng float64) (string, error) {
		return "", errors.New("simulated geocode outage")
	}
	defer func() { testGeocoder.fn = nil }()

	resp, result := doServiceabilityRequest(t, auth.AccessToken, map[string]interface{}{
		"latitude": 12.975, "longitude": 77.640,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 even when geocoding fails, got %d: %v", resp.StatusCode, result)
	}

	data := result["data"].(map[string]interface{})
	if data["serviceable"].(bool) != true {
		t.Fatal("location should still be serviceable when geocoding fails")
	}
	if data["darkstore_id"].(string) != "DS-TEST-1" {
		t.Fatalf("expected darkstore_id DS-TEST-1, got %v", data["darkstore_id"])
	}
	if _, present := data["resolved_address"]; present {
		t.Fatalf("resolved_address should be omitted when geocoding fails, got %v", data["resolved_address"])
	}
}
