//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamodbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

// uniqueSuffix returns a process-unique string for building test identifiers.
func uniqueSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// ── helpers ──────────────────────────────────────────────────────────────────

// registerDEWithReferral registers a DE optionally passing a referral code and
// returns the full register response. Fails the test on non-201.
func registerDEWithReferral(t *testing.T, phone, referralCode string) map[string]interface{} {
	t.Helper()
	body := map[string]interface{}{
		"phone_number": phone,
		"name":         "Referral DE",
		"profile_url":  "https://example.com/p.jpg",
		"nrc_url":      "https://example.com/n.jpg",
	}
	if referralCode != "" {
		body["referral_code"] = referralCode
	}
	resp, result := doRequest(t, "POST", "/api/v1/de/register", nil, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("registerDEWithReferral(%s): expected 201, got %d: %v", phone, resp.StatusCode, result)
	}
	return result
}

// waitForReferralCode polls the ReferralCodeIndex GSI until the given code is
// queryable, guarding against any GSI propagation lag on DynamoDB Local.
func waitForReferralCode(t *testing.T, code string) {
	t.Helper()
	for i := 0; i < 50; i++ {
		out, err := dynamoClient.Query(context.Background(), &dynamodb.QueryInput{
			TableName:              aws.String(testTableName),
			IndexName:              aws.String("ReferralCodeIndex"),
			KeyConditionExpression: aws.String("referral_code = :c"),
			ExpressionAttributeValues: map[string]dynamodbtypes.AttributeValue{
				":c": &dynamodbtypes.AttributeValueMemberS{Value: code},
			},
			Limit: aws.Int32(1),
		})
		if err == nil && len(out.Items) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("referral code %q never became queryable on ReferralCodeIndex", code)
}

// newReferralService builds a ReferralService backed by the live test DynamoDB.
// Used to exercise the bonus loop, which has no HTTP route yet.
func newReferralService() *service.ReferralService {
	logger := logrus.New()
	logger.SetLevel(logrus.PanicLevel)
	referralRepo := repository.NewReferralRepository(dynamoClient, testTableName, logger)
	deRepo := repository.NewDERepository(dynamoClient, testTableName, logger)
	payoutConfigRepo := repository.NewPayoutConfigRepository(dynamoClient, testTableName, logger)
	return service.NewReferralService(referralRepo, deRepo, payoutConfigRepo, logger)
}

// seedPayoutConfig writes the CONFIG/PAYOUT_V1 item with the referral knobs.
func seedPayoutConfig(t *testing.T, tripsThreshold, windowDays int, bonusZMW string) {
	t.Helper()
	_, err := dynamoClient.PutItem(context.Background(), &dynamodb.PutItemInput{
		TableName: aws.String(testTableName),
		Item: map[string]dynamodbtypes.AttributeValue{
			"PK":                       &dynamodbtypes.AttributeValueMemberS{Value: "CONFIG"},
			"SK":                       &dynamodbtypes.AttributeValueMemberS{Value: "PAYOUT_V1"},
			"referral_trips_threshold": &dynamodbtypes.AttributeValueMemberN{Value: strconv.Itoa(tripsThreshold)},
			"referral_window_days":     &dynamodbtypes.AttributeValueMemberN{Value: strconv.Itoa(windowDays)},
			"referral_bonus_zmw":       &dynamodbtypes.AttributeValueMemberN{Value: bonusZMW},
		},
	})
	if err != nil {
		t.Fatalf("seedPayoutConfig: %v", err)
	}
}

// seedReferral writes a referral item directly, allowing control of created_at
// and status to simulate windows that the repository's Create cannot produce.
func seedReferral(t *testing.T, referrerID, referredID string, status models.ReferralStatus, createdAt time.Time) {
	t.Helper()
	_, err := dynamoClient.PutItem(context.Background(), &dynamodb.PutItemInput{
		TableName: aws.String(testTableName),
		Item: map[string]dynamodbtypes.AttributeValue{
			"PK":                &dynamodbtypes.AttributeValueMemberS{Value: "REFERRAL!" + referredID},
			"SK":                &dynamodbtypes.AttributeValueMemberS{Value: "METADATA"},
			"referrer_de_id":    &dynamodbtypes.AttributeValueMemberS{Value: referrerID},
			"referred_de_id":    &dynamodbtypes.AttributeValueMemberS{Value: referredID},
			"status":            &dynamodbtypes.AttributeValueMemberS{Value: string(status)},
			"created_at":        &dynamodbtypes.AttributeValueMemberS{Value: createdAt.UTC().Format(time.RFC3339)},
			"window_expires_at": &dynamodbtypes.AttributeValueMemberS{Value: createdAt.Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)},
		},
	})
	if err != nil {
		t.Fatalf("seedReferral: %v", err)
	}
}

func getReferralStatus(t *testing.T, referredID string) string {
	t.Helper()
	out, err := dynamoClient.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: aws.String(testTableName),
		Key: map[string]dynamodbtypes.AttributeValue{
			"PK": &dynamodbtypes.AttributeValueMemberS{Value: "REFERRAL!" + referredID},
			"SK": &dynamodbtypes.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil || out.Item == nil {
		t.Fatalf("getReferralStatus: referral not found for %s (err=%v)", referredID, err)
	}
	return out.Item["status"].(*dynamodbtypes.AttributeValueMemberS).Value
}

// ── HTTP: registration produces a referral code ───────────────────────────────

func TestReferral_RegisterReturnsCode(t *testing.T) {
	res := registerDEWithReferral(t, uniquePhone("90"), "")
	code, ok := res["referral_code"].(string)
	if !ok || code == "" {
		t.Fatalf("expected referral_code in register response, got %v", res["referral_code"])
	}
	if len(code) != 6 {
		t.Fatalf("expected 6-digit referral code, got %q (len %d)", code, len(code))
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			t.Fatalf("expected numeric referral code, got %q", code)
		}
	}
}

func TestReferral_CodesAreUniquePerDE(t *testing.T) {
	a := registerDEWithReferral(t, uniquePhone("91"), "")["referral_code"].(string)
	b := registerDEWithReferral(t, uniquePhone("92"), "")["referral_code"].(string)
	if a == b {
		t.Fatalf("expected distinct referral codes, both were %q", a)
	}
}

// ── HTTP: linking + referral screen ───────────────────────────────────────────

func TestReferral_LinkOnRegisterAndScreen(t *testing.T) {
	referrerPhone := uniquePhone("93")
	referrer := registerDEWithReferral(t, referrerPhone, "")
	code := referrer["referral_code"].(string)
	waitForReferralCode(t, code)

	referredPhone := uniquePhone("94")
	referred := registerDEWithReferral(t, referredPhone, code)
	referredID := referred["de_id"].(string)

	// Referrer logs in and views their referral screen
	tokens := authenticateDE(t, referrerPhone)
	resp, result := doRequest(t, "GET", "/api/v1/de/referral", bearerHeaders(tokens.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /de/referral: expected 200, got %d: %v", resp.StatusCode, result)
	}
	if result["referral_code"].(string) != code {
		t.Fatalf("expected referral_code %q on screen, got %v", code, result["referral_code"])
	}
	referrals, ok := result["referrals"].([]interface{})
	if !ok || len(referrals) != 1 {
		t.Fatalf("expected exactly 1 referral, got %v", result["referrals"])
	}
	item := referrals[0].(map[string]interface{})
	if item["status"] != "active" {
		t.Fatalf("expected status=active, got %v", item["status"])
	}
	if item["referred_de_id"] != referredID {
		t.Fatalf("expected referred_de_id=%s, got %v", referredID, item["referred_de_id"])
	}
}

func TestReferral_InvalidCodeIsNonFatal(t *testing.T) {
	phone := uniquePhone("95")
	res := registerDEWithReferral(t, phone, "000001") // code that belongs to nobody
	deID := res["de_id"].(string)

	// Registration still succeeds and the DE gets its own code
	if res["referral_code"].(string) == "" {
		t.Fatal("expected new DE to still receive a referral code")
	}

	// No referral relationship should have been created for this DE
	out, err := dynamoClient.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: aws.String(testTableName),
		Key: map[string]dynamodbtypes.AttributeValue{
			"PK": &dynamodbtypes.AttributeValueMemberS{Value: "REFERRAL!" + deID},
			"SK": &dynamodbtypes.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if out.Item != nil {
		t.Fatal("expected no referral item for DE registered with an invalid code")
	}
}

func TestReferral_ScreenRequiresAuth(t *testing.T) {
	resp, _ := doRequest(t, "GET", "/api/v1/de/referral", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}
}

func TestReferral_ScreenRejectsCustomerToken(t *testing.T) {
	tokens := authenticateUser(t, uniquePhone("96"))
	resp, _ := doRequest(t, "GET", "/api/v1/de/referral", bearerHeaders(tokens.AccessToken), nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for customer token, got %d", resp.StatusCode)
	}
}

// ── HTTP: payout config PATCH ─────────────────────────────────────────────────

func TestConfigPayout_UpdateNumericField(t *testing.T) {
	resp, result := doRequest(t, "PATCH", "/api/v1/config/payout", nil, map[string]interface{}{
		"field": "referral_bonus_zmw",
		"value": "42.5",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", resp.StatusCode, result)
	}
	if result["status"] != "updated" {
		t.Fatalf("expected status=updated, got %v", result["status"])
	}

	out, err := dynamoClient.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: aws.String(testTableName),
		Key: map[string]dynamodbtypes.AttributeValue{
			"PK": &dynamodbtypes.AttributeValueMemberS{Value: "CONFIG"},
			"SK": &dynamodbtypes.AttributeValueMemberS{Value: "PAYOUT_V1"},
		},
	})
	if err != nil || out.Item == nil {
		t.Fatalf("config item not found after PATCH (err=%v)", err)
	}
	n, ok := out.Item["referral_bonus_zmw"].(*dynamodbtypes.AttributeValueMemberN)
	if !ok || n.Value != "42.5" {
		t.Fatalf("expected referral_bonus_zmw=42.5 (Number), got %v", out.Item["referral_bonus_zmw"])
	}
}

func TestConfigPayout_MissingFieldRejected(t *testing.T) {
	resp, _ := doRequest(t, "PATCH", "/api/v1/config/payout", nil, map[string]interface{}{
		"value": "10",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing field, got %d", resp.StatusCode)
	}
}

// ── Service: the bonus loop (no HTTP route — Plan A wires it at trip completion) ─

func TestCheckAndTriggerBonus_BelowThreshold(t *testing.T) {
	seedPayoutConfig(t, 5, 30, "50")
	svc := newReferralService()
	referredID := "referred-below-" + uniqueSuffix()
	seedReferral(t, "referrer-x", referredID, models.ReferralStatusActive, time.Now())

	bonus, referrer, err := svc.CheckAndTriggerBonus(context.Background(), referredID, 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bonus != 0 || referrer != "" {
		t.Fatalf("expected no bonus below threshold, got bonus=%v referrer=%q", bonus, referrer)
	}
	if got := getReferralStatus(t, referredID); got != "active" {
		t.Fatalf("expected referral to stay active, got %q", got)
	}
}

func TestCheckAndTriggerBonus_TriggersAtThreshold(t *testing.T) {
	seedPayoutConfig(t, 5, 30, "50")
	svc := newReferralService()
	referredID := "referred-hit-" + uniqueSuffix()
	seedReferral(t, "referrer-hit", referredID, models.ReferralStatusActive, time.Now())

	bonus, referrer, err := svc.CheckAndTriggerBonus(context.Background(), referredID, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bonus != 50 {
		t.Fatalf("expected bonus 50, got %v", bonus)
	}
	if referrer != "referrer-hit" {
		t.Fatalf("expected referrer-hit, got %q", referrer)
	}
	if got := getReferralStatus(t, referredID); got != "completed" {
		t.Fatalf("expected referral status=completed after trigger, got %q", got)
	}
}

func TestCheckAndTriggerBonus_NoDoubleTrigger(t *testing.T) {
	seedPayoutConfig(t, 5, 30, "50")
	svc := newReferralService()
	referredID := "referred-double-" + uniqueSuffix()
	seedReferral(t, "referrer-d", referredID, models.ReferralStatusActive, time.Now())

	if bonus, _, err := svc.CheckAndTriggerBonus(context.Background(), referredID, 6); err != nil || bonus != 50 {
		t.Fatalf("first trigger: expected bonus 50, got %v (err=%v)", bonus, err)
	}
	// Second call must not pay out again
	bonus, referrer, err := svc.CheckAndTriggerBonus(context.Background(), referredID, 7)
	if err != nil {
		t.Fatalf("second trigger unexpected error: %v", err)
	}
	if bonus != 0 || referrer != "" {
		t.Fatalf("expected no second bonus, got bonus=%v referrer=%q", bonus, referrer)
	}
}

func TestCheckAndTriggerBonus_ExpiredWindow(t *testing.T) {
	seedPayoutConfig(t, 5, 30, "50")
	svc := newReferralService()
	referredID := "referred-expired-" + uniqueSuffix()
	// Created 40 days ago — outside the 30-day window
	seedReferral(t, "referrer-e", referredID, models.ReferralStatusActive, time.Now().Add(-40*24*time.Hour))

	bonus, referrer, err := svc.CheckAndTriggerBonus(context.Background(), referredID, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bonus != 0 || referrer != "" {
		t.Fatalf("expected no bonus for expired window, got bonus=%v referrer=%q", bonus, referrer)
	}
	if got := getReferralStatus(t, referredID); got != "active" {
		t.Fatalf("expected referral to remain active (not consumed), got %q", got)
	}
}

func TestCheckAndTriggerBonus_NoReferral(t *testing.T) {
	seedPayoutConfig(t, 5, 30, "50")
	svc := newReferralService()

	bonus, referrer, err := svc.CheckAndTriggerBonus(context.Background(), "de-with-no-referral-"+uniqueSuffix(), 99)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bonus != 0 || referrer != "" {
		t.Fatalf("expected no bonus when DE has no referral, got bonus=%v referrer=%q", bonus, referrer)
	}
}

// ── Service: end-to-end link → trigger using real repositories ────────────────

func TestReferral_LinkThenTriggerEndToEnd(t *testing.T) {
	seedPayoutConfig(t, 3, 30, "75")
	svc := newReferralService()

	// Register a referrer through HTTP so it lands on the GSI
	referrerPhone := uniquePhone("97")
	referrer := registerDEWithReferral(t, referrerPhone, "")
	referrerID := referrer["de_id"].(string)
	code := referrer["referral_code"].(string)
	waitForReferralCode(t, code)

	// Register a referred DE with the code
	referred := registerDEWithReferral(t, uniquePhone("98"), code)
	referredID := referred["de_id"].(string)

	// Drive the referred DE to the trip threshold
	bonus, gotReferrer, err := svc.CheckAndTriggerBonus(context.Background(), referredID, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bonus != 75 {
		t.Fatalf("expected bonus 75, got %v", bonus)
	}
	if gotReferrer != referrerID {
		t.Fatalf("expected referrer %s, got %s", referrerID, gotReferrer)
	}
	if got := getReferralStatus(t, referredID); got != "completed" {
		t.Fatalf("expected completed, got %q", got)
	}
}
