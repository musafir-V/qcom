package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

type stubTwilioVerifier struct {
	startErr error
	checkOK  bool
	checkErr error
	started  []string
	checked  []string
}

func (s *stubTwilioVerifier) StartSMSVerification(_ context.Context, phoneNumber string) error {
	s.started = append(s.started, phoneNumber)
	return s.startErr
}

func (s *stubTwilioVerifier) CheckVerification(_ context.Context, phoneNumber, code string) (bool, error) {
	s.checked = append(s.checked, phoneNumber+":"+code)
	return s.checkOK, s.checkErr
}

type stubAfricaTalkingSender struct {
	configured bool
	sendErr    error
	sent       []string // "phone:otp"
}

func (s *stubAfricaTalkingSender) Configured() bool { return s.configured }

func (s *stubAfricaTalkingSender) SendOTP(_ context.Context, phoneNumber, otp string) error {
	s.sent = append(s.sent, phoneNumber+":"+otp)
	return s.sendErr
}

type stubOTPStore struct {
	mu    sync.Mutex
	data  map[string]models.OTPData
	gets  int
	stores int
	deletes int
}

func newStubOTPStore() *stubOTPStore {
	return &stubOTPStore{data: make(map[string]models.OTPData)}
}

func (s *stubOTPStore) Store(_ context.Context, phoneNumber string, otpData models.OTPData) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stores++
	s.data[phoneNumber] = otpData
	return nil
}

func (s *stubOTPStore) Get(_ context.Context, phoneNumber string) (*models.OTPData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	d, ok := s.data[phoneNumber]
	if !ok {
		return nil, errors.New("OTP not found or expired")
	}
	cp := d
	return &cp, nil
}

func (s *stubOTPStore) Delete(_ context.Context, phoneNumber string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes++
	delete(s.data, phoneNumber)
	return nil
}

func (s *stubOTPStore) get(phone string) (models.OTPData, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.data[phone]
	return d, ok
}

type stubRoutingConfig struct {
	forceTwilio bool
	err         error
}

func (s *stubRoutingConfig) Get(_ context.Context) (*models.SMSOTPRoutingConfig, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &models.SMSOTPRoutingConfig{ForceTwilio: s.forceTwilio}, nil
}

func testOTPCfg() *config.OTPConfig {
	return &config.OTPConfig{
		Length:      6,
		Expiry:      10 * time.Minute,
		MaxAttempts: 5,
	}
}

func newTestOTPService(
	twilio *stubTwilioVerifier,
	at *stubAfricaTalkingSender,
	store *stubOTPStore,
	routing *stubRoutingConfig,
) *OTPService {
	var atSender africaTalkingOTPSender
	if at != nil {
		atSender = at
	}
	var route smsOTPRoutingConfig
	if routing != nil {
		route = routing
	}
	var otpRepo otpStore
	if store != nil {
		otpRepo = store
	}
	return &OTPService{
		twilio:  twilio,
		at:      atSender,
		otpRepo: otpRepo,
		routing: route,
		cfg:     testOTPCfg(),
		logger:  logrus.New(),
	}
}

func TestGenerateOTP_Digit2_UsesTwilio(t *testing.T) {
	twilio := &stubTwilioVerifier{}
	at := &stubAfricaTalkingSender{configured: true}
	store := newStubOTPStore()
	svc := newTestOTPService(twilio, at, store, &stubRoutingConfig{forceTwilio: false})

	otp, err := svc.GenerateOTP(context.Background(), "+260770021112")
	if err != nil {
		t.Fatalf("GenerateOTP error: %v", err)
	}
	if otp != "" {
		t.Fatalf("otp = %q, want empty", otp)
	}
	if len(twilio.started) != 1 {
		t.Fatalf("twilio started = %v, want 1 call", twilio.started)
	}
	if len(at.sent) != 0 {
		t.Fatalf("AT sent = %v, want none", at.sent)
	}
}

func TestGenerateOTP_Digit0_ATConfigured_SendsAndStores(t *testing.T) {
	twilio := &stubTwilioVerifier{}
	at := &stubAfricaTalkingSender{configured: true}
	store := newStubOTPStore()
	svc := newTestOTPService(twilio, at, store, &stubRoutingConfig{forceTwilio: false})

	otp, err := svc.GenerateOTP(context.Background(), "+260770021110")
	if err != nil {
		t.Fatalf("GenerateOTP error: %v", err)
	}
	if otp != "" {
		t.Fatalf("otp = %q, want empty (not exposed to API)", otp)
	}
	if len(twilio.started) != 0 {
		t.Fatalf("twilio started = %v, want none", twilio.started)
	}
	if len(at.sent) != 1 {
		t.Fatalf("AT sent = %v, want 1 call", at.sent)
	}
	stored, ok := store.get("+260770021110")
	if !ok {
		t.Fatal("expected OTP stored after successful AT send")
	}
	if stored.OTP == "" || stored.OTPHash == "" {
		t.Fatalf("stored OTP incomplete: %+v", stored)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(stored.OTPHash), []byte(stored.OTP)); err != nil {
		t.Fatalf("stored hash does not match plaintext: %v", err)
	}
}

func TestGenerateOTP_Digit0_ATFails_TwilioFailoverAndLocalDeleted(t *testing.T) {
	twilio := &stubTwilioVerifier{}
	at := &stubAfricaTalkingSender{configured: true, sendErr: errors.New("at down")}
	store := newStubOTPStore()
	// Pre-seed a stale local OTP that should be cleared on failover.
	_ = store.Store(context.Background(), "+260770021110", models.OTPData{
		OTP: "111111", OTPHash: "x", Phone: "+260770021110",
		ExpiresAt: time.Now().Add(time.Minute),
	})
	svc := newTestOTPService(twilio, at, store, &stubRoutingConfig{forceTwilio: false})

	otp, err := svc.GenerateOTP(context.Background(), "+260770021110")
	if err != nil {
		t.Fatalf("GenerateOTP error: %v", err)
	}
	if otp != "" {
		t.Fatalf("otp = %q, want empty", otp)
	}
	if len(at.sent) != 1 {
		t.Fatalf("AT send attempts = %d, want 1", len(at.sent))
	}
	if len(twilio.started) != 1 {
		t.Fatalf("twilio started = %v, want failover call", twilio.started)
	}
	if _, ok := store.get("+260770021110"); ok {
		t.Fatal("local OTP should be deleted on AT→Twilio failover")
	}
}

func TestGenerateOTP_ForceTwilio_UsesTwilioEvenForDigit0(t *testing.T) {
	twilio := &stubTwilioVerifier{}
	at := &stubAfricaTalkingSender{configured: true}
	store := newStubOTPStore()
	svc := newTestOTPService(twilio, at, store, &stubRoutingConfig{forceTwilio: true})

	_, err := svc.GenerateOTP(context.Background(), "+260770021110")
	if err != nil {
		t.Fatalf("GenerateOTP error: %v", err)
	}
	if len(at.sent) != 0 {
		t.Fatalf("AT sent = %v, want none when force_twilio", at.sent)
	}
	if len(twilio.started) != 1 {
		t.Fatalf("twilio started = %v, want 1", twilio.started)
	}
}

func TestGenerateOTP_ResendReusesOTP(t *testing.T) {
	twilio := &stubTwilioVerifier{}
	at := &stubAfricaTalkingSender{configured: true}
	store := newStubOTPStore()
	svc := newTestOTPService(twilio, at, store, &stubRoutingConfig{forceTwilio: false})

	phone := "+260770021111"
	_, err := svc.GenerateOTP(context.Background(), phone)
	if err != nil {
		t.Fatalf("first GenerateOTP: %v", err)
	}
	first, ok := store.get(phone)
	if !ok {
		t.Fatal("expected stored OTP after first send")
	}
	storesAfterFirst := store.stores

	_, err = svc.GenerateOTP(context.Background(), phone)
	if err != nil {
		t.Fatalf("resend GenerateOTP: %v", err)
	}
	if len(at.sent) != 2 {
		t.Fatalf("AT sent count = %d, want 2", len(at.sent))
	}
	if at.sent[0] != at.sent[1] {
		t.Fatalf("resend should reuse OTP; got %q then %q", at.sent[0], at.sent[1])
	}
	second, ok := store.get(phone)
	if !ok {
		t.Fatal("OTP should still be stored after resend")
	}
	if first.OTP != second.OTP {
		t.Fatalf("OTP changed on resend: %q → %q", first.OTP, second.OTP)
	}
	if store.stores != storesAfterFirst {
		t.Fatalf("resend should not re-store; stores=%d want %d", store.stores, storesAfterFirst)
	}
	if len(twilio.started) != 0 {
		t.Fatalf("twilio should not be used on successful AT resend")
	}
}

func TestGenerateOTP_SoftSuccessWhenBothFail(t *testing.T) {
	twilio := &stubTwilioVerifier{startErr: errors.New("twilio down")}
	at := &stubAfricaTalkingSender{configured: true, sendErr: errors.New("at down")}
	store := newStubOTPStore()
	svc := newTestOTPService(twilio, at, store, &stubRoutingConfig{forceTwilio: false})

	otp, err := svc.GenerateOTP(context.Background(), "+260770021110")
	if err != nil {
		t.Fatalf("expected soft success, got error: %v", err)
	}
	if otp != "" {
		t.Fatalf("otp = %q, want empty", otp)
	}
	if len(twilio.started) != 1 {
		t.Fatalf("expected Twilio failover attempt, got %v", twilio.started)
	}
}

func TestVerifyOTP_LocalSuccess(t *testing.T) {
	twilio := &stubTwilioVerifier{}
	at := &stubAfricaTalkingSender{configured: true}
	store := newStubOTPStore()
	svc := newTestOTPService(twilio, at, store, &stubRoutingConfig{forceTwilio: false})

	phone := "+260770021110"
	plain := "654321"
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Store(context.Background(), phone, models.OTPData{
		OTP: plain, OTPHash: string(hash), Phone: phone,
		Attempts: 0, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute),
	})

	valid, err := svc.VerifyOTP(context.Background(), phone, plain)
	if err != nil {
		t.Fatalf("VerifyOTP error: %v", err)
	}
	if !valid {
		t.Fatal("expected local OTP to verify")
	}
	if len(twilio.checked) != 0 {
		t.Fatalf("twilio should not be called for local verify; checked=%v", twilio.checked)
	}
	if _, ok := store.get(phone); ok {
		t.Fatal("local OTP should be deleted after successful verify")
	}
}

func TestVerifyOTP_ForceTwilioIgnoresLocal(t *testing.T) {
	twilio := &stubTwilioVerifier{checkOK: true}
	at := &stubAfricaTalkingSender{configured: true}
	store := newStubOTPStore()
	svc := newTestOTPService(twilio, at, store, &stubRoutingConfig{forceTwilio: true})

	phone := "+260770021110"
	plain := "654321"
	hash, _ := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.MinCost)
	_ = store.Store(context.Background(), phone, models.OTPData{
		OTP: plain, OTPHash: string(hash), Phone: phone,
		ExpiresAt: time.Now().Add(time.Minute),
	})

	valid, err := svc.VerifyOTP(context.Background(), phone, "999999")
	if err != nil {
		t.Fatalf("VerifyOTP error: %v", err)
	}
	if !valid {
		t.Fatal("expected Twilio check result")
	}
	if len(twilio.checked) != 1 {
		t.Fatalf("expected Twilio CheckVerification; checked=%v", twilio.checked)
	}
	// Local row left alone (force path ignores it); still present.
	if _, ok := store.get(phone); !ok {
		t.Fatal("force_twilio path should not delete local OTP as part of verify")
	}
}

func TestVerifyOTP_MasterBypass(t *testing.T) {
	svc := newTestOTPService(&stubTwilioVerifier{}, &stubAfricaTalkingSender{}, newStubOTPStore(), &stubRoutingConfig{})

	valid, err := svc.VerifyOTP(context.Background(), "+260770990572", masterOTPBypass)
	if err != nil {
		t.Fatalf("VerifyOTP error: %v", err)
	}
	if !valid {
		t.Fatal("expected master OTP bypass to succeed")
	}
	if masterOTPBypass != "221133" {
		t.Fatalf("masterOTPBypass = %q, want 221133", masterOTPBypass)
	}
}

func TestVerifyOTP_Digit0_NoLocal_FallsBackToTwilio(t *testing.T) {
	twilio := &stubTwilioVerifier{checkOK: true}
	svc := newTestOTPService(twilio, &stubAfricaTalkingSender{configured: true}, newStubOTPStore(), &stubRoutingConfig{})

	valid, err := svc.VerifyOTP(context.Background(), "+260770021110", "123456")
	if err != nil {
		t.Fatalf("VerifyOTP error: %v", err)
	}
	if !valid {
		t.Fatal("expected Twilio fallback success")
	}
	if len(twilio.checked) != 1 {
		t.Fatalf("checked=%v, want Twilio fallback", twilio.checked)
	}
}

func TestForceTwilio_OnRoutingGetError(t *testing.T) {
	twilio := &stubTwilioVerifier{}
	at := &stubAfricaTalkingSender{configured: true}
	store := newStubOTPStore()
	svc := newTestOTPService(twilio, at, store, &stubRoutingConfig{err: errors.New("ddb down")})

	_, err := svc.GenerateOTP(context.Background(), "+260770021110")
	if err != nil {
		t.Fatalf("GenerateOTP error: %v", err)
	}
	if len(at.sent) != 0 {
		t.Fatalf("config error should fail open to Twilio; AT sent=%v", at.sent)
	}
	if len(twilio.started) != 1 {
		t.Fatalf("twilio started=%v, want 1", twilio.started)
	}
}

func TestGenerateOTP_TwilioPathDeletesLocalBeforeStart(t *testing.T) {
	twilio := &stubTwilioVerifier{}
	store := newStubOTPStore()
	_ = store.Store(context.Background(), "+260770021112", models.OTPData{
		OTP: "111111", OTPHash: "x", Phone: "+260770021112",
		ExpiresAt: time.Now().Add(time.Minute),
	})
	svc := newTestOTPService(twilio, &stubAfricaTalkingSender{configured: true}, store, &stubRoutingConfig{})

	_, err := svc.GenerateOTP(context.Background(), "+260770021112")
	if err != nil {
		t.Fatalf("GenerateOTP error: %v", err)
	}
	if _, ok := store.get("+260770021112"); ok {
		t.Fatal("local OTP should be deleted before Twilio StartSMSVerification")
	}
}

func TestGenerateOTP_Digit1_UsesAfricaTalking(t *testing.T) {
	twilio := &stubTwilioVerifier{}
	at := &stubAfricaTalkingSender{configured: true}
	store := newStubOTPStore()
	svc := newTestOTPService(twilio, at, store, &stubRoutingConfig{forceTwilio: false})

	_, err := svc.GenerateOTP(context.Background(), "+260770021111")
	if err != nil {
		t.Fatalf("GenerateOTP error: %v", err)
	}
	if len(at.sent) != 1 {
		t.Fatalf("AT sent = %v, want 1", at.sent)
	}
	if len(twilio.started) != 0 {
		t.Fatalf("twilio started = %v, want none", twilio.started)
	}
}

func TestGenerateOTP_ATNotConfigured_UsesTwilioForDigit0(t *testing.T) {
	twilio := &stubTwilioVerifier{}
	at := &stubAfricaTalkingSender{configured: false}
	store := newStubOTPStore()
	svc := newTestOTPService(twilio, at, store, &stubRoutingConfig{forceTwilio: false})

	_, err := svc.GenerateOTP(context.Background(), "+260770021110")
	if err != nil {
		t.Fatalf("GenerateOTP error: %v", err)
	}
	if len(at.sent) != 0 {
		t.Fatalf("AT sent = %v, want none when unconfigured", at.sent)
	}
	if len(twilio.started) != 1 {
		t.Fatalf("twilio started = %v, want 1", twilio.started)
	}
}

func TestGenerateOTP_NilAT_UsesTwilioForDigit0(t *testing.T) {
	twilio := &stubTwilioVerifier{}
	store := newStubOTPStore()
	svc := newTestOTPService(twilio, nil, store, &stubRoutingConfig{forceTwilio: false})

	_, err := svc.GenerateOTP(context.Background(), "+260770021110")
	if err != nil {
		t.Fatalf("GenerateOTP error: %v", err)
	}
	if len(twilio.started) != 1 {
		t.Fatalf("twilio started = %v, want 1", twilio.started)
	}
}

func TestGenerateOTP_NilRouting_FailsOpenToTwilio(t *testing.T) {
	twilio := &stubTwilioVerifier{}
	at := &stubAfricaTalkingSender{configured: true}
	store := newStubOTPStore()
	svc := newTestOTPService(twilio, at, store, nil)

	_, err := svc.GenerateOTP(context.Background(), "+260770021110")
	if err != nil {
		t.Fatalf("GenerateOTP error: %v", err)
	}
	if len(at.sent) != 0 {
		t.Fatalf("nil routing should force Twilio; AT sent=%v", at.sent)
	}
	if len(twilio.started) != 1 {
		t.Fatalf("twilio started=%v, want 1", twilio.started)
	}
}

func TestGenerateOTP_ResendATFails_TwilioFailover(t *testing.T) {
	twilio := &stubTwilioVerifier{}
	at := &stubAfricaTalkingSender{configured: true}
	store := newStubOTPStore()
	svc := newTestOTPService(twilio, at, store, &stubRoutingConfig{forceTwilio: false})

	phone := "+260770021110"
	_, err := svc.GenerateOTP(context.Background(), phone)
	if err != nil {
		t.Fatalf("first GenerateOTP: %v", err)
	}
	first, ok := store.get(phone)
	if !ok || first.OTP == "" {
		t.Fatal("expected stored OTP after first send")
	}

	at.sendErr = errors.New("at down on resend")
	_, err = svc.GenerateOTP(context.Background(), phone)
	if err != nil {
		t.Fatalf("resend GenerateOTP: %v", err)
	}
	if len(twilio.started) != 1 {
		t.Fatalf("expected Twilio failover on resend failure; started=%v", twilio.started)
	}
	if _, ok := store.get(phone); ok {
		t.Fatal("local OTP should be deleted after resend AT failure → Twilio failover")
	}
}

func TestGenerateOTP_ExpiredLocal_MintsNewOTP(t *testing.T) {
	twilio := &stubTwilioVerifier{}
	at := &stubAfricaTalkingSender{configured: true}
	store := newStubOTPStore()
	phone := "+260770021110"
	_ = store.Store(context.Background(), phone, models.OTPData{
		OTP: "111111", OTPHash: "x", Phone: phone,
		ExpiresAt: time.Now().Add(-time.Minute),
	})
	svc := newTestOTPService(twilio, at, store, &stubRoutingConfig{forceTwilio: false})

	_, err := svc.GenerateOTP(context.Background(), phone)
	if err != nil {
		t.Fatalf("GenerateOTP error: %v", err)
	}
	stored, ok := store.get(phone)
	if !ok {
		t.Fatal("expected new OTP stored")
	}
	if stored.OTP == "111111" {
		t.Fatal("expired OTP should not be reused")
	}
	if len(at.sent) != 1 || !strings.HasSuffix(at.sent[0], ":"+stored.OTP) {
		t.Fatalf("AT sent=%v, stored=%q", at.sent, stored.OTP)
	}
}

func TestVerifyOTP_LocalInvalid_IncrementsAttempts(t *testing.T) {
	twilio := &stubTwilioVerifier{}
	store := newStubOTPStore()
	svc := newTestOTPService(twilio, &stubAfricaTalkingSender{configured: true}, store, &stubRoutingConfig{})

	phone := "+260770021110"
	hash, err := bcrypt.GenerateFromPassword([]byte("654321"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Store(context.Background(), phone, models.OTPData{
		OTP: "654321", OTPHash: string(hash), Phone: phone,
		Attempts: 0, ExpiresAt: time.Now().Add(time.Minute),
	})

	valid, err := svc.VerifyOTP(context.Background(), phone, "000000")
	if err == nil || valid {
		t.Fatalf("expected invalid OTP error, valid=%v err=%v", valid, err)
	}
	stored, ok := store.get(phone)
	if !ok {
		t.Fatal("OTP should remain stored after failed attempt")
	}
	if stored.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", stored.Attempts)
	}
	if len(twilio.checked) != 0 {
		t.Fatalf("twilio should not be called; checked=%v", twilio.checked)
	}
}

func TestVerifyOTP_LocalExpired(t *testing.T) {
	store := newStubOTPStore()
	svc := newTestOTPService(&stubTwilioVerifier{}, &stubAfricaTalkingSender{configured: true}, store, &stubRoutingConfig{})

	phone := "+260770021110"
	hash, _ := bcrypt.GenerateFromPassword([]byte("654321"), bcrypt.MinCost)
	_ = store.Store(context.Background(), phone, models.OTPData{
		OTP: "654321", OTPHash: string(hash), Phone: phone,
		ExpiresAt: time.Now().Add(-time.Second),
	})

	valid, err := svc.VerifyOTP(context.Background(), phone, "654321")
	if valid || err == nil {
		t.Fatalf("expected expired error, valid=%v err=%v", valid, err)
	}
	if _, ok := store.get(phone); ok {
		t.Fatal("expired OTP should be deleted")
	}
}

func TestVerifyOTP_LocalMaxAttempts(t *testing.T) {
	store := newStubOTPStore()
	svc := newTestOTPService(&stubTwilioVerifier{}, &stubAfricaTalkingSender{configured: true}, store, &stubRoutingConfig{})

	phone := "+260770021110"
	hash, _ := bcrypt.GenerateFromPassword([]byte("654321"), bcrypt.MinCost)
	_ = store.Store(context.Background(), phone, models.OTPData{
		OTP: "654321", OTPHash: string(hash), Phone: phone,
		Attempts: 5, ExpiresAt: time.Now().Add(time.Minute),
	})

	valid, err := svc.VerifyOTP(context.Background(), phone, "654321")
	if valid || err == nil {
		t.Fatalf("expected max attempts error, valid=%v err=%v", valid, err)
	}
	if _, ok := store.get(phone); ok {
		t.Fatal("OTP should be deleted after max attempts")
	}
}

func TestVerifyOTP_Digit2_UsesTwilioEvenIfLocalExists(t *testing.T) {
	twilio := &stubTwilioVerifier{checkOK: true}
	store := newStubOTPStore()
	phone := "+260770021112"
	hash, _ := bcrypt.GenerateFromPassword([]byte("654321"), bcrypt.MinCost)
	_ = store.Store(context.Background(), phone, models.OTPData{
		OTP: "654321", OTPHash: string(hash), Phone: phone,
		ExpiresAt: time.Now().Add(time.Minute),
	})
	svc := newTestOTPService(twilio, &stubAfricaTalkingSender{configured: true}, store, &stubRoutingConfig{})

	valid, err := svc.VerifyOTP(context.Background(), phone, "999999")
	if err != nil {
		t.Fatalf("VerifyOTP error: %v", err)
	}
	if !valid {
		t.Fatal("expected Twilio check result")
	}
	if len(twilio.checked) != 1 {
		t.Fatalf("digit 2 should verify via Twilio; checked=%v", twilio.checked)
	}
}

func TestVerifyOTP_TwilioRejected(t *testing.T) {
	twilio := &stubTwilioVerifier{checkOK: false}
	svc := newTestOTPService(twilio, &stubAfricaTalkingSender{}, newStubOTPStore(), &stubRoutingConfig{})

	valid, err := svc.VerifyOTP(context.Background(), "+260770021112", "123456")
	if valid || err == nil {
		t.Fatalf("expected invalid OTP, valid=%v err=%v", valid, err)
	}
}

func TestVerifyOTP_TwilioCheckError(t *testing.T) {
	twilio := &stubTwilioVerifier{checkErr: errors.New("twilio boom")}
	svc := newTestOTPService(twilio, &stubAfricaTalkingSender{}, newStubOTPStore(), &stubRoutingConfig{})

	valid, err := svc.VerifyOTP(context.Background(), "+260770021112", "123456")
	if valid || err == nil {
		t.Fatalf("expected error, valid=%v err=%v", valid, err)
	}
}

func TestIsOTPReusable(t *testing.T) {
	now := time.Now()
	ok := isOTPReusable(&models.OTPData{
		OTP: "123456", ExpiresAt: now.Add(time.Minute), Attempts: 0,
	}, now, 5)
	if !ok {
		t.Fatal("expected reusable")
	}
	if isOTPReusable(&models.OTPData{
		OTP: "", ExpiresAt: now.Add(time.Minute),
	}, now, 5) {
		t.Fatal("empty plaintext should not be reusable")
	}
	if isOTPReusable(&models.OTPData{
		OTP: "1", ExpiresAt: now.Add(-time.Second),
	}, now, 5) {
		t.Fatal("expired should not be reusable")
	}
	if isOTPReusable(&models.OTPData{
		OTP: "1", ExpiresAt: now.Add(time.Minute), Attempts: 5,
	}, now, 5) {
		t.Fatal("max attempts should not be reusable")
	}
}

func TestGenerateRandomOTP_Length(t *testing.T) {
	otp, err := generateRandomOTP(6)
	if err != nil {
		t.Fatalf("generateRandomOTP: %v", err)
	}
	if len(otp) != 6 {
		t.Fatalf("len=%d, want 6", len(otp))
	}
	for _, c := range otp {
		if c < '0' || c > '9' {
			t.Fatalf("non-digit in OTP: %q", otp)
		}
	}
}
