//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamodbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

const (
	testVonageAppID        = "test-vonage-app-id"
	testVonageWhatsAppFrom = "15559615672"
)

var vonageOTPSends = make(map[string][]string)

func recordVonageOTPSend(phone, otp string) {
	vonageOTPSends[phone] = append(vonageOTPSends[phone], otp)
}

func getVonageOTPSends(phone string) []string {
	return append([]string(nil), vonageOTPSends[phone]...)
}

func resetVonageOTPSends(phone string) {
	delete(vonageOTPSends, phone)
}

func testVonagePrivateKeyB64() string {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(fmt.Sprintf("generate test vonage key: %v", err))
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	return base64.StdEncoding.EncodeToString(pem.EncodeToMemory(block))
}

func newTestVonageJWTRepository(logger *logrus.Logger) *repository.VonageJWTRepository {
	return repository.NewVonageJWTRepository(dynamoClient, testTableName, logger)
}

func newTestVonageService(t *testing.T, jwtRepo *repository.VonageJWTRepository, messagesURL string, httpClient *http.Client) *service.VonageService {
	t.Helper()

	cfg := &config.VonageConfig{
		AppID:         testVonageAppID,
		PrivateKeyB64: testVonagePrivateKeyB64(),
		WhatsAppFrom:  testVonageWhatsAppFrom,
	}
	logger := logrus.New()
	logger.SetLevel(logrus.ErrorLevel)

	svc := service.NewVonageService(cfg, jwtRepo, logger)
	svc.SetMessagesURL(messagesURL)
	if httpClient != nil {
		svc.SetHTTPClient(httpClient)
	}
	return svc
}

func seedVonageJWTCache(t *testing.T, token string, ttl time.Time) {
	t.Helper()

	_, err := dynamoClient.PutItem(context.Background(), &dynamodb.PutItemInput{
		TableName: aws.String(testTableName),
		Item: map[string]dynamodbtypes.AttributeValue{
			"PK":    &dynamodbtypes.AttributeValueMemberS{Value: "VONAGE_JWT"},
			"SK":    &dynamodbtypes.AttributeValueMemberS{Value: "TOKEN"},
			"Token": &dynamodbtypes.AttributeValueMemberS{Value: token},
			"TTL":   &dynamodbtypes.AttributeValueMemberN{Value: strconv.FormatInt(ttl.Unix(), 10)},
		},
	})
	if err != nil {
		t.Fatalf("seed vonage jwt cache: %v", err)
	}
}

func getVonageJWTCacheToken(t *testing.T) string {
	t.Helper()

	result, err := dynamoClient.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: aws.String(testTableName),
		Key: map[string]dynamodbtypes.AttributeValue{
			"PK": &dynamodbtypes.AttributeValueMemberS{Value: "VONAGE_JWT"},
			"SK": &dynamodbtypes.AttributeValueMemberS{Value: "TOKEN"},
		},
	})
	if err != nil {
		t.Fatalf("get vonage jwt cache: %v", err)
	}
	if result.Item == nil {
		return ""
	}
	attr, ok := result.Item["Token"].(*dynamodbtypes.AttributeValueMemberS)
	if !ok {
		return ""
	}
	return attr.Value
}

func deleteVonageJWTCache(t *testing.T) {
	t.Helper()

	_, err := dynamoClient.DeleteItem(context.Background(), &dynamodb.DeleteItemInput{
		TableName: aws.String(testTableName),
		Key: map[string]dynamodbtypes.AttributeValue{
			"PK": &dynamodbtypes.AttributeValueMemberS{Value: "VONAGE_JWT"},
			"SK": &dynamodbtypes.AttributeValueMemberS{Value: "TOKEN"},
		},
	})
	if err != nil {
		t.Fatalf("delete vonage jwt cache: %v", err)
	}
}

func storeOTPTest(phone, otp string) error {
	_, err := dynamoClient.PutItem(context.Background(), &dynamodb.PutItemInput{
		TableName: aws.String(testTableName),
		Item: map[string]dynamodbtypes.AttributeValue{
			"PK":  &dynamodbtypes.AttributeValueMemberS{Value: fmt.Sprintf("OTP_TEST#%s", phone)},
			"SK":  &dynamodbtypes.AttributeValueMemberS{Value: "METADATA"},
			"OTP": &dynamodbtypes.AttributeValueMemberS{Value: otp},
		},
	})
	return err
}

func extractOTPFromVonageBody(body map[string]interface{}) (string, error) {
	custom, ok := body["custom"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("missing custom payload")
	}
	template, ok := custom["template"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("missing template payload")
	}
	components, ok := template["components"].([]interface{})
	if !ok || len(components) == 0 {
		return "", fmt.Errorf("missing template components")
	}
	first, ok := components[0].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid first component")
	}
	params, ok := first["parameters"].([]interface{})
	if !ok || len(params) == 0 {
		return "", fmt.Errorf("missing body parameters")
	}
	param, ok := params[0].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid body parameter")
	}
	otp, ok := param["text"].(string)
	if !ok || otp == "" {
		return "", fmt.Errorf("missing otp text")
	}
	return otp, nil
}

// newSuccessVonageMockServer accepts all message sends and stores OTP_TEST rows
// so existing auth integration tests can still read OTPs from DynamoDB.
func newSuccessVonageMockServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var body map[string]interface{}
		if err := json.Unmarshal(raw, &body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		to, _ := body["to"].(string)
		otp, err := extractOTPFromVonageBody(body)
		if err == nil && to != "" {
			phone := "+" + to
			_ = storeOTPTest(phone, otp)
			recordVonageOTPSend(phone, otp)
		}

		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"message_uuid": "test-message-uuid"})
	}))
}
