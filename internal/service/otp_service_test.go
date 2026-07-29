package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

type stubOTPRepo struct {
	data      map[string]models.OTPData
	storeErr  error
	deleteErr error
}

func (s *stubOTPRepo) Store(_ context.Context, phoneNumber string, otpData models.OTPData) error {
	if s.storeErr != nil {
		return s.storeErr
	}
	if s.data == nil {
		s.data = make(map[string]models.OTPData)
	}
	s.data[phoneNumber] = otpData
	return nil
}

func (s *stubOTPRepo) Get(_ context.Context, phoneNumber string) (*models.OTPData, error) {
	data, ok := s.data[phoneNumber]
	if !ok {
		return nil, repository.ErrOTPNotFound
	}
	copy := data
	return &copy, nil
}

func (s *stubOTPRepo) Delete(_ context.Context, phoneNumber string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.data, phoneNumber)
	return nil
}

type stubWhatsAppSender struct {
	sent []string
	err  error
}

func (s *stubWhatsAppSender) SendWhatsAppOTP(_ context.Context, _, otp string) error {
	s.sent = append(s.sent, otp)
	return s.err
}

func newTestOTPService(repo *stubOTPRepo, sender *stubWhatsAppSender) *OTPService {
	return &OTPService{
		otpRepo:       repo,
		vonageService: sender,
		cfg: &config.OTPConfig{
			Length:      6,
			Expiry:      10 * time.Minute,
			MaxAttempts: 5,
		},
		logger: logrus.New(),
	}
}

func TestGenerateOTP_ReusesValidExistingOTP(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("123456"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash otp: %v", err)
	}

	repo := &stubOTPRepo{
		data: map[string]models.OTPData{
			"+919515365236": {
				OTP:       "123456",
				OTPHash:   string(hash),
				Phone:     "+919515365236",
				Attempts:  0,
				CreatedAt: time.Now().Add(-2 * time.Minute),
				ExpiresAt: time.Now().Add(8 * time.Minute),
			},
		},
	}
	sender := &stubWhatsAppSender{}

	svc := newTestOTPService(repo, sender)
	otp, err := svc.GenerateOTP(context.Background(), "+919515365236")
	if err != nil {
		t.Fatalf("GenerateOTP returned error: %v", err)
	}
	if otp != "123456" {
		t.Fatalf("otp = %q, want 123456", otp)
	}
	if len(sender.sent) != 1 || sender.sent[0] != "123456" {
		t.Fatalf("sent = %v, want [123456]", sender.sent)
	}
	if stored := repo.data["+919515365236"]; stored.OTP != "123456" {
		t.Fatalf("stored OTP changed to %q", stored.OTP)
	}
}

func TestGenerateOTP_CreatesNewOTPWhenExpired(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("123456"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash otp: %v", err)
	}

	repo := &stubOTPRepo{
		data: map[string]models.OTPData{
			"+919515365236": {
				OTP:       "123456",
				OTPHash:   string(hash),
				Phone:     "+919515365236",
				Attempts:  0,
				CreatedAt: time.Now().Add(-15 * time.Minute),
				ExpiresAt: time.Now().Add(-5 * time.Minute),
			},
		},
	}
	sender := &stubWhatsAppSender{}

	svc := newTestOTPService(repo, sender)
	otp, err := svc.GenerateOTP(context.Background(), "+919515365236")
	if err != nil {
		t.Fatalf("GenerateOTP returned error: %v", err)
	}
	if otp == "123456" {
		t.Fatal("expected a newly generated OTP")
	}
	if len(otp) != 6 {
		t.Fatalf("otp length = %d, want 6", len(otp))
	}
	if len(sender.sent) != 1 || sender.sent[0] != otp {
		t.Fatalf("sent = %v, want [%s]", sender.sent, otp)
	}
	if stored := repo.data["+919515365236"]; stored.OTP != otp {
		t.Fatalf("stored OTP = %q, want %q", stored.OTP, otp)
	}
}

func TestGenerateOTP_CreatesNewOTPWhenPlaintextMissing(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("123456"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash otp: %v", err)
	}

	repo := &stubOTPRepo{
		data: map[string]models.OTPData{
			"+919515365236": {
				OTPHash:   string(hash),
				Phone:     "+919515365236",
				Attempts:  0,
				CreatedAt: time.Now(),
				ExpiresAt: time.Now().Add(10 * time.Minute),
			},
		},
	}
	sender := &stubWhatsAppSender{}

	svc := newTestOTPService(repo, sender)
	otp, err := svc.GenerateOTP(context.Background(), "+919515365236")
	if err != nil {
		t.Fatalf("GenerateOTP returned error: %v", err)
	}
	if otp == "" {
		t.Fatal("expected generated OTP")
	}
	if stored := repo.data["+919515365236"]; stored.OTP != otp {
		t.Fatalf("stored OTP = %q, want %q", stored.OTP, otp)
	}
}

func TestVerifyOTP_MasterBypass(t *testing.T) {
	svc := newTestOTPService(&stubOTPRepo{}, &stubWhatsAppSender{})

	valid, err := svc.VerifyOTP(context.Background(), "+919515365236", masterOTPBypass)
	if err != nil {
		t.Fatalf("VerifyOTP returned error: %v", err)
	}
	if !valid {
		t.Fatal("expected master OTP bypass to succeed")
	}
}

func TestIsOTPReusable(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		data *models.OTPData
		want bool
	}{
		{
			name: "valid",
			data: &models.OTPData{OTP: "123456", ExpiresAt: now.Add(time.Minute), Attempts: 0},
			want: true,
		},
		{
			name: "expired",
			data: &models.OTPData{OTP: "123456", ExpiresAt: now.Add(-time.Minute), Attempts: 0},
			want: false,
		},
		{
			name: "max attempts",
			data: &models.OTPData{OTP: "123456", ExpiresAt: now.Add(time.Minute), Attempts: 5},
			want: false,
		},
		{
			name: "missing plaintext",
			data: &models.OTPData{ExpiresAt: now.Add(time.Minute), Attempts: 0},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isOTPReusable(tt.data, now, 5); got != tt.want {
				t.Fatalf("isOTPReusable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func storedOTPFixture(t *testing.T, phone string) *stubOTPRepo {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("123456"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash otp: %v", err)
	}
	return &stubOTPRepo{data: map[string]models.OTPData{
		phone: {
			OTP:       "123456",
			OTPHash:   string(hash),
			Phone:     phone,
			ExpiresAt: time.Now().Add(5 * time.Minute),
		},
	}}
}

func TestVerifyOTP_AttemptCounterWriteFailurePropagates(t *testing.T) {
	const phone = "+919515365236"
	repo := storedOTPFixture(t, phone)
	repo.storeErr = errors.New("dynamodb down")

	valid, err := newTestOTPService(repo, &stubWhatsAppSender{}).VerifyOTP(context.Background(), phone, "000000")
	if valid {
		t.Fatal("expected verification to fail")
	}
	if err == nil || IsOTPRejection(err) {
		t.Fatalf("err = %v, want a non-rejection infrastructure error", err)
	}
}

func TestVerifyOTP_ConsumeFailurePropagates(t *testing.T) {
	const phone = "+919515365236"
	repo := storedOTPFixture(t, phone)
	repo.deleteErr = errors.New("dynamodb down")

	valid, err := newTestOTPService(repo, &stubWhatsAppSender{}).VerifyOTP(context.Background(), phone, "123456")
	if valid {
		t.Fatal("expected verification to fail when the OTP cannot be consumed")
	}
	if err == nil || IsOTPRejection(err) {
		t.Fatalf("err = %v, want a non-rejection infrastructure error", err)
	}
}

func TestVerifyOTP_RejectionsAreClassified(t *testing.T) {
	const phone = "+919515365236"
	tests := []struct {
		name string
		repo *stubOTPRepo
		otp  string
	}{
		{name: "not found", repo: &stubOTPRepo{}, otp: "123456"},
		{name: "wrong otp", repo: storedOTPFixture(t, phone), otp: "000000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			valid, err := newTestOTPService(tt.repo, &stubWhatsAppSender{}).VerifyOTP(context.Background(), phone, tt.otp)
			if valid {
				t.Fatal("expected verification to fail")
			}
			if !IsOTPRejection(err) {
				t.Fatalf("err = %v, want an OTP rejection", err)
			}
		})
	}
}
