//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamodbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/timezone"
)

// appendEarningEntry writes a ledger entry directly to DynamoDB for test setup.
func appendEarningEntry(t *testing.T, deID string, amount float64, createdAt string, earningType models.EarningType) {
	t.Helper()

	entry := &models.EarningsLedger{
		DEID:        deID,
		EarningID:   uuid.New().String(),
		Type:        earningType,
		AmountZMW:   amount,
		CreatedAt:   createdAt,
		ReferenceID: uuid.New().String(),
	}
	item, err := attributevalue.MarshalMap(entry)
	if err != nil {
		t.Fatalf("marshal earning: %v", err)
	}
	item["PK"] = &dynamodbtypes.AttributeValueMemberS{Value: entry.GetPK()}
	item["SK"] = &dynamodbtypes.AttributeValueMemberS{Value: entry.GetSK()}

	_, err = dynamoClient.PutItem(context.Background(), &dynamodb.PutItemInput{
		TableName: aws.String(testTableName),
		Item:      item,
	})
	if err != nil {
		t.Fatalf("appendEarningEntry: %v", err)
	}
}

func getDELastDisbursedAt(t *testing.T, phone string) string {
	t.Helper()
	out, err := dynamoClient.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: aws.String(testTableName),
		Key: map[string]dynamodbtypes.AttributeValue{
			"PK": &dynamodbtypes.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &dynamodbtypes.AttributeValueMemberS{Value: "METADATA"},
		},
		ProjectionExpression: aws.String("last_disbursed_at"),
	})
	if err != nil {
		t.Fatalf("getDELastDisbursedAt: %v", err)
	}
	if v, ok := out.Item["last_disbursed_at"]; ok {
		return v.(*dynamodbtypes.AttributeValueMemberS).Value
	}
	return ""
}

func recordDisbursement(t *testing.T, deID, phone string, body map[string]interface{}) (*http.Response, map[string]interface{}) {
	t.Helper()
	if body == nil {
		body = map[string]interface{}{
			"amount_zmw":  500.0,
			"period_from":   "2026-06-01",
			"period_to":     "2026-06-14",
			"de_phone":      phone,
		}
	}
	return doRequest(t, "POST", fmt.Sprintf("/api/v1/de/%s/disbursement", deID), nil, body)
}

func TestDisbursement_RequiresDEPhone(t *testing.T) {
	phone := uniquePhone("92")
	deID := registerDE(t, phone)

	resp, result := recordDisbursement(t, deID, phone, map[string]interface{}{
		"amount_zmw":  100,
		"period_from": "2026-06-01",
		"period_to":   "2026-06-14",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}
	errObj, ok := result["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected error object, got %v", result)
	}
	if errObj["code"].(string) != "MISSING_FIELD" {
		t.Fatalf("expected MISSING_FIELD, got %v", errObj["code"])
	}
}

func TestDisbursement_DEIDMismatch(t *testing.T) {
	phone := uniquePhone("93")
	registerDE(t, phone)

	resp, result := recordDisbursement(t, "wrong-de-id", phone, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"].(string) != "DE_ID_MISMATCH" {
		t.Fatalf("expected DE_ID_MISMATCH, got %v", errObj["code"])
	}
}

func TestDisbursement_InvalidAmount(t *testing.T) {
	phone := uniquePhone("94")
	deID := registerDE(t, phone)

	resp, _ := recordDisbursement(t, deID, phone, map[string]interface{}{
		"amount_zmw":  0,
		"period_from": "2026-06-01",
		"period_to":   "2026-06-14",
		"de_phone":    phone,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestDisbursement_InvalidPeriodOrder(t *testing.T) {
	phone := uniquePhone("95")
	deID := registerDE(t, phone)

	resp, result := recordDisbursement(t, deID, phone, map[string]interface{}{
		"amount_zmw":  100,
		"period_from": "2026-06-15",
		"period_to":   "2026-06-01",
		"de_phone":    phone,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %v", resp.StatusCode, result)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"].(string) != "INVALID_PERIOD" {
		t.Fatalf("expected INVALID_PERIOD, got %v", errObj["code"])
	}
}

// TestDisbursement_FullCycle verifies disbursement resets outstanding balance and
// that earnings recorded after payout remain visible on the earnings screen.
func TestDisbursement_FullCycle(t *testing.T) {
	phone := uniquePhone("96")
	deID := registerDE(t, phone)
	tokens := authenticateDE(t, phone)

	appendEarningEntry(t, deID, 100, "2026-01-01T10:00:00+02:00", models.EarningTypeTrip)
	appendEarningEntry(t, deID, 50, "2026-01-01T11:00:00+02:00", models.EarningTypeTrip)
	appendEarningEntry(t, deID, 25, "2026-01-01T12:00:00+02:00", models.EarningTypeWeeklyBonus)

	resp, summary := doRequest(t, "GET", "/api/v1/de/earnings/summary", bearerHeaders(tokens.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("summary before disbursement: %d %v", resp.StatusCode, summary)
	}
	if bal := summary["outstanding_balance_zmw"].(float64); bal != 175 {
		t.Fatalf("before disbursement: expected 175, got %v", bal)
	}
	if live := summary["live_order_total_zmw"].(float64); live != 150 {
		t.Fatalf("before disbursement live: expected 150, got %v", live)
	}
	if bonus := summary["bonus_total_zmw"].(float64); bonus != 25 {
		t.Fatalf("before disbursement bonus: expected 25, got %v", bonus)
	}

	// period_to is far in the future — watermark must still be disbursed_at, not period end.
	resp, disb := recordDisbursement(t, deID, phone, map[string]interface{}{
		"amount_zmw":  175,
		"period_from":   "2026-01-01",
		"period_to":     "2099-12-31",
		"de_phone":      phone,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("disbursement: expected 201, got %d: %v", resp.StatusCode, disb)
	}
	disbursedAt := disb["disbursed_at"].(string)
	if disbursedAt == "" {
		t.Fatal("missing disbursed_at in response")
	}

	watermark := getDELastDisbursedAt(t, phone)
	if watermark != disbursedAt {
		t.Fatalf("last_disbursed_at = %q, want disbursed_at %q (must not use period_to)", watermark, disbursedAt)
	}

	resp, summary = doRequest(t, "GET", "/api/v1/de/earnings/summary", bearerHeaders(tokens.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("summary after disbursement: %d %v", resp.StatusCode, summary)
	}
	if bal := summary["outstanding_balance_zmw"].(float64); bal != 0 {
		t.Fatalf("after disbursement: expected 0 outstanding, got %v", bal)
	}

	// Earning after payout must appear on the earnings screen.
	time.Sleep(10 * time.Millisecond) // ensure created_at strictly follows disbursed_at
	afterPayout := timezone.Now().Format(time.RFC3339)
	appendEarningEntry(t, deID, 55, afterPayout, models.EarningTypeTrip)

	resp, summary = doRequest(t, "GET", "/api/v1/de/earnings/summary", bearerHeaders(tokens.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("summary after new earning: %d %v", resp.StatusCode, summary)
	}
	if bal := summary["outstanding_balance_zmw"].(float64); bal != 55 {
		t.Fatalf("after new earning: expected 55 outstanding, got %v", bal)
	}

	items, ok := summary["line_items"].([]interface{})
	if !ok || len(items) != 1 {
		t.Fatalf("expected 1 line item, got %v", summary["line_items"])
	}
}

// TestDisbursement_AppearsInHistory verifies the DE can see recorded disbursements.
func TestDisbursement_AppearsInHistory(t *testing.T) {
	phone := uniquePhone("97")
	deID := registerDE(t, phone)
	tokens := authenticateDE(t, phone)

	resp, disb := recordDisbursement(t, deID, phone, map[string]interface{}{
		"amount_zmw":  880,
		"period_from":   "2026-06-07",
		"period_to":     "2026-06-14",
		"de_phone":      phone,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("disbursement: %d %v", resp.StatusCode, disb)
	}

	resp, result := doRequest(t, "GET", "/api/v1/de/earnings/disbursements", bearerHeaders(tokens.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disbursements list: %d %v", resp.StatusCode, result)
	}
	list, ok := result["disbursements"].([]interface{})
	if !ok || len(list) != 1 {
		t.Fatalf("expected 1 disbursement, got %v", result["disbursements"])
	}
	first := list[0].(map[string]interface{})
	if first["amount_zmw"].(float64) != 880 {
		t.Fatalf("amount mismatch: %v", first["amount_zmw"])
	}
	if first["period_from"].(string) != "2026-06-07" {
		t.Fatalf("period_from mismatch: %v", first["period_from"])
	}
	if first["period_to"].(string) != "2026-06-14" {
		t.Fatalf("period_to mismatch: %v", first["period_to"])
	}
}

// TestDisbursement_SecondPayout verifies a second disbursement further clears balance.
func TestDisbursement_SecondPayout(t *testing.T) {
	phone := uniquePhone("98")
	deID := registerDE(t, phone)
	tokens := authenticateDE(t, phone)

	appendEarningEntry(t, deID, 200, "2026-02-01T10:00:00+02:00", models.EarningTypeTrip)

	resp, _ := recordDisbursement(t, deID, phone, map[string]interface{}{
		"amount_zmw":  200,
		"period_from":   "2026-02-01",
		"period_to":     "2026-02-01",
		"de_phone":      phone,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first disbursement failed: %d", resp.StatusCode)
	}

	watermark := getDELastDisbursedAt(t, phone)
	wmTime, err := time.Parse(time.RFC3339, watermark)
	if err != nil {
		t.Fatalf("parse watermark: %v", err)
	}
	// Must be strictly after first payout watermark (RFC3339 has second precision).
	appendEarningEntry(t, deID, 75, wmTime.Add(time.Second).Format(time.RFC3339), models.EarningTypeTrip)

	resp, summary := doRequest(t, "GET", "/api/v1/de/earnings/summary", bearerHeaders(tokens.AccessToken), nil)
	if bal := summary["outstanding_balance_zmw"].(float64); bal != 75 {
		t.Fatalf("between payouts: expected 75, got %v", bal)
	}

	time.Sleep(1100 * time.Millisecond) // advance past earning timestamp before second payout
	resp, _ = recordDisbursement(t, deID, phone, map[string]interface{}{
		"amount_zmw":  75,
		"period_from":   "2026-02-02",
		"period_to":     "2026-02-02",
		"de_phone":      phone,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("second disbursement failed: %d", resp.StatusCode)
	}

	resp, summary = doRequest(t, "GET", "/api/v1/de/earnings/summary", bearerHeaders(tokens.AccessToken), nil)
	if bal := summary["outstanding_balance_zmw"].(float64); bal != 0 {
		t.Fatalf("after second payout: expected 0, got %v", bal)
	}

	resp, result := doRequest(t, "GET", "/api/v1/de/earnings/disbursements", bearerHeaders(tokens.AccessToken), nil)
	list := result["disbursements"].([]interface{})
	if len(list) != 2 {
		t.Fatalf("expected 2 disbursements, got %d", len(list))
	}
}

// TestDisbursement_TodayEarningsUnaffected verifies /de/me today_earnings still
// counts today's ledger entries regardless of last_disbursed_at.
func TestDisbursement_TodayEarningsUnaffected(t *testing.T) {
	phone := uniquePhone("99")
	deID := registerDE(t, phone)
	tokens := authenticateDE(t, phone)

	todayMorning := timezone.StartOfDayString()
	appendEarningEntry(t, deID, 100, todayMorning, models.EarningTypeTrip)

	resp, me := doRequest(t, "GET", "/api/v1/de/me", bearerHeaders(tokens.AccessToken), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/de/me: %d %v", resp.StatusCode, me)
	}
	if te := me["today_earnings_zmw"].(float64); te != 100 {
		t.Fatalf("before disbursement today_earnings: expected 100, got %v", te)
	}

	resp, _ = recordDisbursement(t, deID, phone, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("disbursement failed: %d", resp.StatusCode)
	}

	resp, summary := doRequest(t, "GET", "/api/v1/de/earnings/summary", bearerHeaders(tokens.AccessToken), nil)
	if bal := summary["outstanding_balance_zmw"].(float64); bal != 0 {
		t.Fatalf("outstanding after payout of today's earnings: expected 0, got %v", bal)
	}

	// today_earnings on home screen still reflects today's total ledger sum.
	resp, me = doRequest(t, "GET", "/api/v1/de/me", bearerHeaders(tokens.AccessToken), nil)
	if te := me["today_earnings_zmw"].(float64); te != 100 {
		t.Fatalf("after disbursement today_earnings: expected 100, got %v", te)
	}
}
