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
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamodbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/models"
)

// setDEInHandCash directly sets a DE's in-hand cash, bypassing business logic.
func setDEInHandCash(t *testing.T, phone string, amount float64) {
	t.Helper()
	_, err := dynamoClient.UpdateItem(context.Background(), &dynamodb.UpdateItemInput{
		TableName: aws.String(testTableName),
		Key: map[string]dynamodbtypes.AttributeValue{
			"PK": &dynamodbtypes.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &dynamodbtypes.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression: aws.String("SET in_hand_cash_zmw = :c"),
		ExpressionAttributeValues: map[string]dynamodbtypes.AttributeValue{
			":c": &dynamodbtypes.AttributeValueMemberN{Value: strconv.FormatFloat(amount, 'f', -1, 64)},
		},
	})
	if err != nil {
		t.Fatalf("setDEInHandCash(%s, %v): %v", phone, amount, err)
	}
}

// getDEInHandCash reads a DE's in-hand cash directly from DynamoDB (0 if unset).
func getDEInHandCash(t *testing.T, phone string) float64 {
	t.Helper()
	out, err := dynamoClient.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: aws.String(testTableName),
		Key: map[string]dynamodbtypes.AttributeValue{
			"PK": &dynamodbtypes.AttributeValueMemberS{Value: "DE!" + phone},
			"SK": &dynamodbtypes.AttributeValueMemberS{Value: "METADATA"},
		},
		ProjectionExpression: aws.String("in_hand_cash_zmw"),
	})
	if err != nil {
		t.Fatalf("getDEInHandCash(%s): %v", phone, err)
	}
	v, ok := out.Item["in_hand_cash_zmw"]
	if !ok {
		return 0
	}
	f, err := strconv.ParseFloat(v.(*dynamodbtypes.AttributeValueMemberN).Value, 64)
	if err != nil {
		t.Fatalf("parse in_hand_cash_zmw: %v", err)
	}
	return f
}

// recordCashDeposit POSTs to the (unauthenticated) ops deposit endpoint.
func recordCashDeposit(t *testing.T, phone string, body map[string]interface{}) (*http.Response, map[string]interface{}) {
	t.Helper()
	return doRequest(t, "POST", fmt.Sprintf("/api/v1/admin/de/%s/cash-deposit", phone), nil, body)
}

// seedCODTripForDE writes an out_for_delivery COD trip (pickup done, drop pending
// with OTP 1234) owned by deID/dePhone, and marks the DE busy on that trip — the
// exact precondition for completing the drop. Returns trip and drop-task IDs.
func seedCODTripForDE(t *testing.T, deID, dePhone string, codAmount float64) (tripID, dropTaskID string) {
	t.Helper()
	tripID = uuid.New().String()
	dropTaskID = uuid.New().String()

	trip := &models.Trip{
		TripID:  tripID,
		OrderID: uuid.New().String(),
		StoreID: testStoreID,
		DEID:    deID,
		DEPhone: dePhone,
		Status:  models.TripStatusOutForDelivery,
		Tasks: []models.Task{
			{TaskID: uuid.New().String(), Type: models.TaskTypePickup, Status: models.TaskStatusCompleted},
			{TaskID: dropTaskID, Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
		},
		Payment:   &models.Payment{CollectCash: true, AmountZMW: codAmount, Currency: "ZMW"},
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	item, err := attributevalue.MarshalMap(trip)
	if err != nil {
		t.Fatalf("marshal trip: %v", err)
	}
	item["PK"] = &dynamodbtypes.AttributeValueMemberS{Value: trip.GetPK()}
	item["SK"] = &dynamodbtypes.AttributeValueMemberS{Value: trip.GetSK()}
	if _, err := dynamoClient.PutItem(context.Background(), &dynamodb.PutItemInput{
		TableName: aws.String(testTableName), Item: item,
	}); err != nil {
		t.Fatalf("seedCODTripForDE put trip: %v", err)
	}

	_, err = dynamoClient.UpdateItem(context.Background(), &dynamodb.UpdateItemInput{
		TableName: aws.String(testTableName),
		Key: map[string]dynamodbtypes.AttributeValue{
			"PK": &dynamodbtypes.AttributeValueMemberS{Value: "DE!" + dePhone},
			"SK": &dynamodbtypes.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression:         aws.String("SET #status = :busy, current_trip_id = :tid, updated_at = :now"),
		ExpressionAttributeNames: map[string]string{"#status": "status"},
		ExpressionAttributeValues: map[string]dynamodbtypes.AttributeValue{
			":busy": &dynamodbtypes.AttributeValueMemberS{Value: "busy"},
			":tid":  &dynamodbtypes.AttributeValueMemberS{Value: tripID},
			":now":  &dynamodbtypes.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339)},
		},
	})
	if err != nil {
		t.Fatalf("seedCODTripForDE set busy: %v", err)
	}
	return tripID, dropTaskID
}

// TestCashAccrual_OnCODDropCompletion verifies completing a COD drop increments
// in-hand cash by the payment amount, atomically with freeing the DE.
func TestCashAccrual_OnCODDropCompletion(t *testing.T) {
	phone := uniquePhone("73")
	deID := registerDE(t, phone)
	tokens := authenticateDE(t, phone)
	auth := bearerHeaders(tokens.AccessToken)

	tripID, dropTaskID := seedCODTripForDE(t, deID, phone, 150)

	resp, result := doRequest(t, "POST",
		fmt.Sprintf("/api/v1/trip/%s/task/%s/status/update", tripID, dropTaskID),
		auth, map[string]interface{}{"status": "completed", "otp": "1234"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete COD drop: expected 200, got %d: %v", resp.StatusCode, result)
	}

	assertDEStatus(t, phone, "free")
	if got := getDEInHandCash(t, phone); got != 150 {
		t.Fatalf("in-hand after COD drop: expected 150, got %v", got)
	}
}

// TestCashAccrual_PrepaidDropDoesNotAccrue verifies a non-COD (prepaid) drop adds
// nothing to in-hand cash.
func TestCashAccrual_PrepaidDropDoesNotAccrue(t *testing.T) {
	phone := uniquePhone("74")
	deID := registerDE(t, phone)
	tokens := authenticateDE(t, phone)
	auth := bearerHeaders(tokens.AccessToken)

	tripID, dropTaskID := seedCODTripForDE(t, deID, phone, 150)
	_, err := dynamoClient.UpdateItem(context.Background(), &dynamodb.UpdateItemInput{
		TableName: aws.String(testTableName),
		Key: map[string]dynamodbtypes.AttributeValue{
			"PK": &dynamodbtypes.AttributeValueMemberS{Value: "TRIP!" + tripID},
			"SK": &dynamodbtypes.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression: aws.String("SET payment.collect_cash = :f"),
		ExpressionAttributeValues: map[string]dynamodbtypes.AttributeValue{
			":f": &dynamodbtypes.AttributeValueMemberBOOL{Value: false},
		},
	})
	if err != nil {
		t.Fatalf("flip payment to prepaid: %v", err)
	}

	resp, result := doRequest(t, "POST",
		fmt.Sprintf("/api/v1/trip/%s/task/%s/status/update", tripID, dropTaskID),
		auth, map[string]interface{}{"status": "completed", "otp": "1234"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete prepaid drop: expected 200, got %d: %v", resp.StatusCode, result)
	}

	assertDEStatus(t, phone, "free")
	if got := getDEInHandCash(t, phone); got != 0 {
		t.Fatalf("in-hand after prepaid drop: expected 0, got %v", got)
	}
}

// TestStartDuty_BlockedWhenOverLimit verifies an over-limit DE cannot start duty
// and gets the CASH_LIMIT_EXCEEDED 409.
func TestStartDuty_BlockedWhenOverLimit(t *testing.T) {
	phone := uniquePhone("75")
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)
	auth := bearerHeaders(tokens.AccessToken)

	setDEInHandCash(t, phone, 600) // default limit is 500

	qrCode := currentQRCode(t, testStoreID)
	resp, result := doRequest(t, "POST", "/api/v1/de/duty/start", auth,
		map[string]interface{}{"qr_code": qrCode})

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %v", resp.StatusCode, result)
	}
	errObj, ok := result["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected error object, got %v", result)
	}
	if errObj["code"].(string) != "CASH_LIMIT_EXCEEDED" {
		t.Fatalf("expected CASH_LIMIT_EXCEEDED, got %v", errObj["code"])
	}
	assertDEStatus(t, phone, "offline") // never became eligible
}

// TestStartDuty_AllowedAtExactLimit verifies the boundary is strict (> not >=):
// exactly at the limit is still allowed.
func TestStartDuty_AllowedAtExactLimit(t *testing.T) {
	phone := uniquePhone("76")
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)
	auth := bearerHeaders(tokens.AccessToken)

	setDEInHandCash(t, phone, 500) // exactly the default limit

	qrCode := currentQRCode(t, testStoreID)
	resp, result := doRequest(t, "POST", "/api/v1/de/duty/start", auth,
		map[string]interface{}{"qr_code": qrCode})

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("at exact limit expected 200, got %d: %v", resp.StatusCode, result)
	}
	assertDEStatus(t, phone, "eligible")
}

// TestDEMe_ExposesCashFields verifies /de/me reports cash_blocked + cash_limit_zmw
// and never the raw in-hand amount.
func TestDEMe_ExposesCashFields(t *testing.T) {
	phone := uniquePhone("77")
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)
	auth := bearerHeaders(tokens.AccessToken)

	setDEInHandCash(t, phone, 600)

	resp, me := doRequest(t, "GET", "/api/v1/de/me", auth, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/de/me: %d %v", resp.StatusCode, me)
	}
	if blocked, _ := me["cash_blocked"].(bool); !blocked {
		t.Fatalf("expected cash_blocked=true, got %v", me["cash_blocked"])
	}
	if lim := me["cash_limit_zmw"].(float64); lim != 500 {
		t.Fatalf("expected cash_limit_zmw=500, got %v", lim)
	}
	if _, leaked := me["in_hand_cash_zmw"]; leaked {
		t.Fatal("/de/me must NOT expose in_hand_cash_zmw to the driver")
	}
}

// TestCashDeposit_PartialThenUnblock verifies a partial deposit decrements the
// counter and a follow-up deposit clears the block so duty start succeeds.
func TestCashDeposit_PartialThenUnblock(t *testing.T) {
	phone := uniquePhone("82")
	registerDE(t, phone)
	tokens := authenticateDE(t, phone)
	auth := bearerHeaders(tokens.AccessToken)

	setDEInHandCash(t, phone, 600)

	// Partial: 600 -> 400 (now under the 500 limit, unblocked).
	resp, result := recordCashDeposit(t, phone, map[string]interface{}{
		"amount_zmw": 200.0, "deposit_id": "dep-partial",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("partial deposit: expected 200, got %d: %v", resp.StatusCode, result)
	}
	if bal := result["in_hand_cash_zmw"].(float64); bal != 400 {
		t.Fatalf("expected new balance 400, got %v", bal)
	}
	if blocked := result["cash_blocked"].(bool); blocked {
		t.Fatalf("expected cash_blocked=false at 400, got true")
	}
	if got := getDEInHandCash(t, phone); got != 400 {
		t.Fatalf("persisted in-hand: expected 400, got %v", got)
	}

	// Now duty start succeeds (in-hand 400 <= 500).
	qrCode := currentQRCode(t, testStoreID)
	resp, du := doRequest(t, "POST", "/api/v1/de/duty/start", auth,
		map[string]interface{}{"qr_code": qrCode})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("duty start after deposit: expected 200, got %d: %v", resp.StatusCode, du)
	}
}

// TestCashDeposit_Idempotent verifies replaying the same deposit_id does not
// double-decrement.
func TestCashDeposit_Idempotent(t *testing.T) {
	phone := uniquePhone("83")
	registerDE(t, phone)

	setDEInHandCash(t, phone, 600)

	resp, _ := recordCashDeposit(t, phone, map[string]interface{}{
		"amount_zmw": 200.0, "deposit_id": "dep-idem",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first deposit: expected 200, got %d", resp.StatusCode)
	}

	// Replay same deposit_id — must be rejected, balance unchanged.
	resp, result := recordCashDeposit(t, phone, map[string]interface{}{
		"amount_zmw": 200.0, "deposit_id": "dep-idem",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("idempotent replay: expected 409, got %d: %v", resp.StatusCode, result)
	}
	if got := getDEInHandCash(t, phone); got != 400 {
		t.Fatalf("balance after replay: expected 400 (no double-decrement), got %v", got)
	}
}

// TestCashDeposit_OverpaymentClampsToZero verifies depositing more than in-hand
// floors the balance at zero.
func TestCashDeposit_OverpaymentClampsToZero(t *testing.T) {
	phone := uniquePhone("84")
	registerDE(t, phone)

	setDEInHandCash(t, phone, 300)

	resp, result := recordCashDeposit(t, phone, map[string]interface{}{
		"amount_zmw": 1000.0, "deposit_id": "dep-over",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("overpayment: expected 200, got %d: %v", resp.StatusCode, result)
	}
	if bal := result["in_hand_cash_zmw"].(float64); bal != 0 {
		t.Fatalf("expected clamped balance 0, got %v", bal)
	}
	if got := getDEInHandCash(t, phone); got != 0 {
		t.Fatalf("persisted balance: expected 0, got %v", got)
	}
}

// TestCashDeposit_InvalidAmount verifies a non-positive amount is rejected.
func TestCashDeposit_InvalidAmount(t *testing.T) {
	phone := uniquePhone("85")
	registerDE(t, phone)

	resp, result := recordCashDeposit(t, phone, map[string]interface{}{
		"amount_zmw": 0.0, "deposit_id": "dep-zero",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("zero amount: expected 400, got %d: %v", resp.StatusCode, result)
	}
	errObj := result["error"].(map[string]interface{})
	if errObj["code"].(string) != "INVALID_AMOUNT" {
		t.Fatalf("expected INVALID_AMOUNT, got %v", errObj["code"])
	}
}
