package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/config"
	"github.com/sirupsen/logrus"
)

func TestAfricaTalkingSMSService_Configured(t *testing.T) {
	if NewAfricaTalkingSMSService(&config.AfricaTalkingConfig{}, logrus.New()).Configured() {
		t.Fatal("expected not configured when credentials empty")
	}
	svc := NewAfricaTalkingSMSService(&config.AfricaTalkingConfig{
		Username: "bunzo",
		APIKey:   "key",
	}, logrus.New())
	if !svc.Configured() {
		t.Fatal("expected configured when username and apiKey set")
	}
}

func TestAfricaTalkingSMSService_SendOTP_PostsJSON(t *testing.T) {
	var gotAPIKey, gotAccept, gotContentType string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("apiKey")
		gotAccept = r.Header.Get("Accept")
		gotContentType = r.Header.Get("Content-Type")
		if r.URL.Path != "/version1/messaging/bulk" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"SMSMessageData":{"Message":"Sent"}}`))
	}))
	defer server.Close()

	svc := NewAfricaTalkingSMSService(&config.AfricaTalkingConfig{
		Username: "bunzo",
		APIKey:   "secret-key",
		BaseURL:  "https://api.africastalking.com",
	}, logrus.New())
	svc.SetBaseURL(server.URL)
	svc.SetHTTPClient(server.Client())

	if err := svc.SendOTP(context.Background(), "+260770990572", "123456"); err != nil {
		t.Fatalf("SendOTP: %v", err)
	}
	if gotAPIKey != "secret-key" {
		t.Fatalf("apiKey header = %q", gotAPIKey)
	}
	if gotAccept != "application/json" {
		t.Fatalf("Accept = %q", gotAccept)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q", gotContentType)
	}
	if gotBody["username"] != "bunzo" {
		t.Fatalf("username = %v", gotBody["username"])
	}
	if gotBody["message"] != "Your OTP to log into Bunzo is 123456" {
		t.Fatalf("message = %v", gotBody["message"])
	}
	phones, ok := gotBody["phoneNumbers"].([]any)
	if !ok || len(phones) != 1 || phones[0] != "+260770990572" {
		t.Fatalf("phoneNumbers = %v", gotBody["phoneNumbers"])
	}
}

func TestAfricaTalkingSMSService_SendOTP_Non2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"InvalidApiKey"}`))
	}))
	defer server.Close()

	svc := NewAfricaTalkingSMSService(&config.AfricaTalkingConfig{
		Username: "bunzo",
		APIKey:   "bad",
	}, logrus.New())
	svc.SetBaseURL(server.URL)
	svc.SetHTTPClient(server.Client())

	err := svc.SendOTP(context.Background(), "+260770990572", "123456")
	if err == nil {
		t.Fatal("expected error for non-2xx")
	}
	if want := "status 401"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want substring %q", err.Error(), want)
	}
	if want := "InvalidApiKey"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want substring %q", err.Error(), want)
	}
}

func TestAfricaTalkingSMSService_NilConfig(t *testing.T) {
	svc := NewAfricaTalkingSMSService(nil, logrus.New())
	if svc.Configured() {
		t.Fatal("nil config should not be configured")
	}
	if svc.baseURL != africaTalkingDefaultBaseURL {
		t.Fatalf("baseURL = %q, want default", svc.baseURL)
	}
}

func TestAfricaTalkingSMSService_Configured_PartialCredentials(t *testing.T) {
	if NewAfricaTalkingSMSService(&config.AfricaTalkingConfig{Username: "bunzo"}, logrus.New()).Configured() {
		t.Fatal("username alone should not be configured")
	}
	if NewAfricaTalkingSMSService(&config.AfricaTalkingConfig{APIKey: "key"}, logrus.New()).Configured() {
		t.Fatal("apiKey alone should not be configured")
	}
}

func TestAfricaTalkingSMSService_SendOTP_TransportError(t *testing.T) {
	svc := NewAfricaTalkingSMSService(&config.AfricaTalkingConfig{
		Username: "bunzo",
		APIKey:   "key",
	}, logrus.New())
	svc.SetBaseURL("http://127.0.0.1:1") // nothing listening
	svc.SetHTTPClient(&http.Client{Timeout: 50 * time.Millisecond})

	err := svc.SendOTP(context.Background(), "+260770990572", "123456")
	if err == nil {
		t.Fatal("expected transport error")
	}
	if !strings.Contains(err.Error(), "africastalking request failed") {
		t.Fatalf("error = %q", err.Error())
	}
}
