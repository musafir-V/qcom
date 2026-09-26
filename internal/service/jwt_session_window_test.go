package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/config"
	"github.com/sirupsen/logrus"
)

func TestSessionWindow_RefreshExpiresAt(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	idle := 2 * time.Hour
	absolute := now.Add(5 * time.Hour)

	exp, ok := (SessionWindow{Idle: idle, Absolute: absolute}).RefreshExpiresAt(now)
	if !ok {
		t.Fatal("expected ok")
	}
	wantIdleEnd := now.Add(idle)
	if d := exp.Sub(wantIdleEnd); d < -time.Second || d > time.Second {
		t.Fatalf("exp=%v want idle end %v", exp, wantIdleEnd)
	}

	shortAbsolute := now.Add(30 * time.Minute)
	exp, ok = (SessionWindow{Idle: idle, Absolute: shortAbsolute}).RefreshExpiresAt(now)
	if !ok {
		t.Fatal("expected ok when idle ends after absolute")
	}
	if !exp.Equal(shortAbsolute) {
		t.Fatalf("exp=%v want absolute %v", exp, shortAbsolute)
	}

	_, ok = (SessionWindow{Idle: idle, Absolute: now}).RefreshExpiresAt(now)
	if ok {
		t.Fatal("expected ok=false when now equals absolute")
	}

	_, ok = (SessionWindow{Idle: idle, Absolute: now.Add(-time.Second)}).RefreshExpiresAt(now)
	if ok {
		t.Fatal("expected ok=false when now is after absolute")
	}
}

func TestGenerateAccessToken_SessionWindowClaims(t *testing.T) {
	accessExpiry := 15 * time.Minute
	refreshExpiry := 60 * 24 * time.Hour
	absoluteExpiry := 365 * 24 * time.Hour
	secret := strings.Repeat("k", 32)
	s, err := NewJWTService(&config.JWTConfig{
		SecretKey:      secret,
		AccessExpiry:   accessExpiry,
		RefreshExpiry:  refreshExpiry,
		AbsoluteExpiry: absoluteExpiry,
	}, logrus.New())
	if err != nil {
		t.Fatal(err)
	}

	before := time.Now()
	pair, _, err := s.GenerateAccessToken("+260700000001", "U1", "customer")
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now()

	refreshClaims, err := s.VerifyToken(pair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	accessClaims, err := s.VerifyToken(pair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}

	if refreshClaims.AbsExp == 0 {
		t.Fatal("expected AbsExp on refresh token")
	}
	if accessClaims.AbsExp != 0 {
		t.Fatalf("access AbsExp=%d want 0", accessClaims.AbsExp)
	}

	abs := time.Unix(refreshClaims.AbsExp, 0)
	if !abs.After(before.Add(absoluteExpiry-5*time.Second)) || !abs.Before(after.Add(absoluteExpiry+5*time.Second)) {
		t.Fatalf("AbsExp=%v not about now+365d", abs)
	}

	refreshExp := refreshClaims.ExpiresAt.Time
	if !refreshExp.After(before.Add(refreshExpiry-5*time.Second)) || !refreshExp.Before(after.Add(refreshExpiry+5*time.Second)) {
		t.Fatalf("refresh exp=%v not about now+60d", refreshExp)
	}

	accessExp := accessClaims.ExpiresAt.Time
	if !accessExp.After(before.Add(accessExpiry-5*time.Second)) || !accessExp.Before(after.Add(accessExpiry+5*time.Second)) {
		t.Fatalf("access exp=%v not about now+15m", accessExp)
	}
}

func TestRefreshTokens_CopiesAbsExpAndSlidesIdle(t *testing.T) {
	s, err := NewJWTService(&config.JWTConfig{
		SecretKey:      strings.Repeat("k", 32),
		AccessExpiry:   15 * time.Minute,
		RefreshExpiry:  time.Hour,
		AbsoluteExpiry: 24 * time.Hour,
	}, logrus.New())
	if err != nil {
		t.Fatal(err)
	}

	pair, familyID, err := s.GenerateAccessToken("+260700000002", "U2", "customer")
	if err != nil {
		t.Fatal(err)
	}
	orig, err := s.VerifyToken(pair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}

	before := time.Now()
	newPair, _, err := s.RefreshTokens(pair.RefreshToken, familyID)
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now()

	refreshed, err := s.VerifyToken(newPair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AbsExp != orig.AbsExp {
		t.Fatalf("AbsExp changed: %d -> %d", orig.AbsExp, refreshed.AbsExp)
	}
	wantExp := before.Add(time.Hour)
	if !refreshed.ExpiresAt.Time.After(wantExp.Add(-5*time.Second)) || !refreshed.ExpiresAt.Time.Before(after.Add(time.Hour+5*time.Second)) {
		t.Fatalf("refresh exp=%v want about now+idle", refreshed.ExpiresAt.Time)
	}
	if !refreshed.ExpiresAt.Time.Before(time.Unix(refreshed.AbsExp, 0)) {
		t.Fatal("refresh exp must stay at or before absolute")
	}
}

func TestRefreshTokens_CapsExpToAbsolute(t *testing.T) {
	s, err := NewJWTService(&config.JWTConfig{
		SecretKey:      strings.Repeat("k", 32),
		AccessExpiry:   15 * time.Minute,
		RefreshExpiry:  24 * time.Hour,
		AbsoluteExpiry: time.Hour,
	}, logrus.New())
	if err != nil {
		t.Fatal(err)
	}

	pair, familyID, err := s.GenerateAccessToken("+260700000003", "U3", "customer")
	if err != nil {
		t.Fatal(err)
	}
	orig, err := s.VerifyToken(pair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}

	newPair, _, err := s.RefreshTokens(pair.RefreshToken, familyID)
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := s.VerifyToken(newPair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AbsExp != orig.AbsExp {
		t.Fatalf("AbsExp changed: %d -> %d", orig.AbsExp, refreshed.AbsExp)
	}
	abs := time.Unix(orig.AbsExp, 0)
	if !refreshed.ExpiresAt.Time.Equal(abs) {
		t.Fatalf("refresh exp=%v want AbsExp %v", refreshed.ExpiresAt.Time, abs)
	}
}

func TestRefreshTokens_AbsoluteExpired(t *testing.T) {
	secret := strings.Repeat("k", 32)
	s, err := NewJWTService(&config.JWTConfig{
		SecretKey:      secret,
		AccessExpiry:   15 * time.Minute,
		RefreshExpiry:  time.Hour,
		AbsoluteExpiry: 24 * time.Hour,
	}, logrus.New())
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	absPast := now.Add(-time.Minute)
	raw := signRefreshToken(t, secret, "+260700000004", "U4", "customer", absPast.Unix(), now.Add(time.Hour))

	pair, familyID, err := s.RefreshTokens(raw, "family-abs-expired")
	if !errors.Is(err, ErrSessionAbsoluteExpired) {
		t.Fatalf("err=%v want ErrSessionAbsoluteExpired", err)
	}
	if pair != nil {
		t.Fatal("expected nil token pair")
	}
	if familyID != "" {
		t.Fatalf("familyID=%q want empty", familyID)
	}
}

func TestRefreshTokens_GrandfathersMissingAbsExp(t *testing.T) {
	secret := strings.Repeat("k", 32)
	absoluteExpiry := 48 * time.Hour
	s, err := NewJWTService(&config.JWTConfig{
		SecretKey:      secret,
		AccessExpiry:   15 * time.Minute,
		RefreshExpiry:  time.Hour,
		AbsoluteExpiry: absoluteExpiry,
	}, logrus.New())
	if err != nil {
		t.Fatal(err)
	}

	raw := signRefreshToken(t, secret, "+260700000005", "U5", "customer", 0, time.Now().Add(time.Hour))

	before := time.Now()
	newPair, _, err := s.RefreshTokens(raw, "family-grandfather")
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now()

	refreshed, err := s.VerifyToken(newPair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AbsExp == 0 {
		t.Fatal("expected AbsExp after grandfathering")
	}
	abs := time.Unix(refreshed.AbsExp, 0)
	if !abs.After(before.Add(absoluteExpiry-5*time.Second)) || !abs.Before(after.Add(absoluteExpiry+5*time.Second)) {
		t.Fatalf("AbsExp=%v not about now+absolute", abs)
	}
}

func signRefreshToken(t *testing.T, secret, phone, entityID, entityType string, absExp int64, expiresAt time.Time) string {
	t.Helper()
	jti := uuid.New().String()
	claims := &Claims{
		Phone:      phone,
		EntityID:   entityID,
		EntityType: entityType,
		Type:       "refresh",
		JTI:        jti,
		AbsExp:     absExp,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   entityID,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			ID:        jti,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return signed
}
