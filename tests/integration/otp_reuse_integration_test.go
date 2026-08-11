//go:build integration

package integration

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

const otpTwilioTestPhone = "+919876543210"

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

func TestInitiateOTP_CallsTwilioVerifyEachTime(t *testing.T) {
	phone := otpTwilioTestPhone
	resetTwilioOTPSends(phone)
	t.Cleanup(func() { resetTwilioOTPSends(phone) })

	initiateOTP(t, phone)
	initiateOTP(t, phone)

	if got := getTwilioOTPSendCount(phone); got != 2 {
		t.Fatalf("twilio send count = %d, want 2", got)
	}
}

func TestInitiateOTP_CanVerifyWithMasterBypass(t *testing.T) {
	phone := "+919876543212"
	resetTwilioOTPSends(phone)
	t.Cleanup(func() { resetTwilioOTPSends(phone) })

	initiateOTP(t, phone)
	tokens := doVerifyOTP(t, phone, "112233", "")
	if tokens.AccessToken == "" {
		t.Fatal("expected access token after master bypass verify")
	}
}
