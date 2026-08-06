package service

import (
	"context"
	"errors"
	"testing"

	"github.com/sirupsen/logrus"
)

type stubTwilioVerifier struct {
	startErr  error
	checkOK   bool
	checkErr  error
	started   []string
	checked   []string
}

func (s *stubTwilioVerifier) StartSMSVerification(_ context.Context, phoneNumber string) error {
	s.started = append(s.started, phoneNumber)
	return s.startErr
}

func (s *stubTwilioVerifier) CheckVerification(_ context.Context, phoneNumber, code string) (bool, error) {
	s.checked = append(s.checked, phoneNumber+":"+code)
	return s.checkOK, s.checkErr
}

func newTestOTPService(twilio *stubTwilioVerifier) *OTPService {
	return &OTPService{
		twilio: twilio,
		logger: logrus.New(),
	}
}

func TestGenerateOTP_StartsTwilioVerification(t *testing.T) {
	twilio := &stubTwilioVerifier{}
	svc := newTestOTPService(twilio)

	otp, err := svc.GenerateOTP(context.Background(), "+260770990572")
	if err != nil {
		t.Fatalf("GenerateOTP returned error: %v", err)
	}
	if otp != "" {
		t.Fatalf("otp = %q, want empty (Twilio owns the code)", otp)
	}
	if len(twilio.started) != 1 || twilio.started[0] != "+260770990572" {
		t.Fatalf("started = %v, want [+260770990572]", twilio.started)
	}
}

func TestGenerateOTP_ReturnsSuccessWhenTwilioFails(t *testing.T) {
	twilio := &stubTwilioVerifier{startErr: errors.New("twilio unavailable")}
	svc := newTestOTPService(twilio)

	otp, err := svc.GenerateOTP(context.Background(), "+260770990572")
	if err != nil {
		t.Fatalf("GenerateOTP returned error: %v", err)
	}
	if otp != "" {
		t.Fatalf("otp = %q, want empty when Twilio fails", otp)
	}
}

func TestVerifyOTP_MasterBypass(t *testing.T) {
	svc := newTestOTPService(&stubTwilioVerifier{})

	valid, err := svc.VerifyOTP(context.Background(), "+260770990572", masterOTPBypass)
	if err != nil {
		t.Fatalf("VerifyOTP returned error: %v", err)
	}
	if !valid {
		t.Fatal("expected master OTP bypass to succeed")
	}
	if masterOTPBypass != "221133" {
		t.Fatalf("masterOTPBypass = %q, want 221133", masterOTPBypass)
	}
}

func TestVerifyOTP_TwilioApproved(t *testing.T) {
	twilio := &stubTwilioVerifier{checkOK: true}
	svc := newTestOTPService(twilio)

	valid, err := svc.VerifyOTP(context.Background(), "+260770990572", "123456")
	if err != nil {
		t.Fatalf("VerifyOTP returned error: %v", err)
	}
	if !valid {
		t.Fatal("expected Twilio-approved OTP to succeed")
	}
}

func TestVerifyOTP_TwilioRejected(t *testing.T) {
	twilio := &stubTwilioVerifier{checkOK: false}
	svc := newTestOTPService(twilio)

	valid, err := svc.VerifyOTP(context.Background(), "+260770990572", "000000")
	if err == nil {
		t.Fatal("expected error for rejected OTP")
	}
	if valid {
		t.Fatal("expected rejected OTP to be invalid")
	}
}
