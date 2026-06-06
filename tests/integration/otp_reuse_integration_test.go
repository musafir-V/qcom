//go:build integration

package integration

import (
	"context"
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

const otpReuseTestPhone = "+919876543210"

func initiateOTP(t *testing.T, phone string) {
	t.Helper()

	body := fmt.Sprintf(`{"phone_number":"%s"}`, phone)
	resp, err := http.Post(testServer.URL+"/api/v1/auth/initiate-otp", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("initiate-otp request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("initiate-otp returned %d: %s", resp.StatusCode, string(raw))
	}
}

func getStoredOTP(t *testing.T, phone string) string {
	t.Helper()

	result, err := dynamoClient.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: aws.String(testTableName),
		Key: map[string]dynamodbtypes.AttributeValue{
			"PK": &dynamodbtypes.AttributeValueMemberS{Value: fmt.Sprintf("OTP#%s", phone)},
			"SK": &dynamodbtypes.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil {
		t.Fatalf("get stored OTP: %v", err)
	}
	if result.Item == nil {
		t.Fatalf("stored OTP not found for %s", phone)
	}

	otpAttr, ok := result.Item["OTP"].(*dynamodbtypes.AttributeValueMemberS)
	if !ok || otpAttr.Value == "" {
		t.Fatalf("OTP plaintext missing for %s", phone)
	}
	return otpAttr.Value
}

func deleteStoredOTP(t *testing.T, phone string) {
	t.Helper()

	for _, pk := range []string{
		fmt.Sprintf("OTP#%s", phone),
		fmt.Sprintf("OTP_TEST#%s", phone),
	} {
		_, err := dynamoClient.DeleteItem(context.Background(), &dynamodb.DeleteItemInput{
			TableName: aws.String(testTableName),
			Key: map[string]dynamodbtypes.AttributeValue{
				"PK": &dynamodbtypes.AttributeValueMemberS{Value: pk},
				"SK": &dynamodbtypes.AttributeValueMemberS{Value: "METADATA"},
			},
		})
		if err != nil {
			t.Fatalf("delete %s: %v", pk, err)
		}
	}
	resetVonageOTPSends(phone)
}

func TestInitiateOTP_SecondCallReusesSameOTPInDynamoDB(t *testing.T) {
	phone := otpReuseTestPhone
	deleteStoredOTP(t, phone)
	t.Cleanup(func() { deleteStoredOTP(t, phone) })

	initiateOTP(t, phone)
	firstOTP := getStoredOTP(t, phone)

	time.Sleep(10 * time.Millisecond)

	initiateOTP(t, phone)
	secondOTP := getStoredOTP(t, phone)

	if firstOTP != secondOTP {
		t.Fatalf("stored OTP changed: first=%s second=%s", firstOTP, secondOTP)
	}
}

func TestInitiateOTP_ResendViaSecondInitiateSendsSameOTPToVonage(t *testing.T) {
	phone := "+919876543211"
	deleteStoredOTP(t, phone)
	t.Cleanup(func() { deleteStoredOTP(t, phone) })

	initiateOTP(t, phone)
	initiateOTP(t, phone)

	sent := getVonageOTPSends(phone)
	if len(sent) != 2 {
		t.Fatalf("vonage send count = %d, want 2", len(sent))
	}
	if sent[0] != sent[1] {
		t.Fatalf("vonage resent different OTP: first=%s second=%s", sent[0], sent[1])
	}
	if sent[0] != getStoredOTP(t, phone) {
		t.Fatalf("vonage OTP %s does not match stored OTP %s", sent[0], getStoredOTP(t, phone))
	}
}

func TestInitiateOTP_ResendCanBeVerifiedWithSameOTP(t *testing.T) {
	phone := "+919876543212"
	deleteStoredOTP(t, phone)
	t.Cleanup(func() { deleteStoredOTP(t, phone) })

	initiateOTP(t, phone)
	otpAfterFirst := getStoredOTP(t, phone)

	initiateOTP(t, phone)

	tokens := doVerifyOTP(t, phone, otpAfterFirst, "")
	if tokens.AccessToken == "" {
		t.Fatal("expected access token after verifying resent OTP")
	}
}
