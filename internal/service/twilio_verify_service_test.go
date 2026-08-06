package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qcom/qcom/internal/config"
	"github.com/sirupsen/logrus"
)

func TestStartSMSVerification_PostsForm(t *testing.T) {
	var gotAuth string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		if r.URL.Path != "/VA123/Verifications" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"status":"pending","sid":"VE123"}`))
	}))
	defer server.Close()

	svc := NewTwilioVerifyService(&config.TwilioConfig{
		AccountSID:       "ACtest",
		AuthToken:        "secret",
		VerifyServiceSID: "VA123",
	}, logrus.New())
	svc.SetBaseURL(server.URL)
	svc.SetHTTPClient(server.Client())

	if err := svc.StartSMSVerification(context.Background(), "+260770990572"); err != nil {
		t.Fatalf("StartSMSVerification: %v", err)
	}
	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Fatalf("Authorization = %q, want Basic", gotAuth)
	}
	if !strings.Contains(gotBody, "To=%2B260770990572") || !strings.Contains(gotBody, "Channel=sms") {
		t.Fatalf("body = %q", gotBody)
	}
}

func TestCheckVerification_Approved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/VA123/VerificationCheck" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"approved","valid":true}`))
	}))
	defer server.Close()

	svc := NewTwilioVerifyService(&config.TwilioConfig{
		AccountSID:       "ACtest",
		AuthToken:        "secret",
		VerifyServiceSID: "VA123",
	}, logrus.New())
	svc.SetBaseURL(server.URL)
	svc.SetHTTPClient(server.Client())

	ok, err := svc.CheckVerification(context.Background(), "+260770990572", "123456")
	if err != nil {
		t.Fatalf("CheckVerification: %v", err)
	}
	if !ok {
		t.Fatal("expected approved")
	}
}

func TestCheckVerification_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":20404}`))
	}))
	defer server.Close()

	svc := NewTwilioVerifyService(&config.TwilioConfig{
		AccountSID:       "ACtest",
		AuthToken:        "secret",
		VerifyServiceSID: "VA123",
	}, logrus.New())
	svc.SetBaseURL(server.URL)
	svc.SetHTTPClient(server.Client())

	ok, err := svc.CheckVerification(context.Background(), "+260770990572", "123456")
	if err != nil {
		t.Fatalf("CheckVerification: %v", err)
	}
	if ok {
		t.Fatal("expected not approved for 404")
	}
}
