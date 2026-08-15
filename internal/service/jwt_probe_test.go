package service

import (
	"strings"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/config"
	"github.com/sirupsen/logrus"
)

func testJWTService(t *testing.T) *JWTService {
	t.Helper()
	s, err := NewJWTService(&config.JWTConfig{
		SecretKey:     strings.Repeat("s", 32),
		AccessExpiry:  15 * time.Minute,
		RefreshExpiry: 7 * 24 * time.Hour,
	}, logrus.New())
	if err != nil {
		t.Fatalf("NewJWTService: %v", err)
	}
	return s
}

func TestProbeRefreshToken_TooShort(t *testing.T) {
	p := ProbeRefreshToken("not-a-jwt", nil)
	if p.LooksLikeJWT {
		t.Fatal("expected not jwt")
	}
	if p.Reason != "too_short" {
		t.Fatalf("reason=%q", p.Reason)
	}
	if p.TokenLen != 9 {
		t.Fatalf("token_len=%d", p.TokenLen)
	}
}

func TestProbeRefreshToken_NotThreeSegments(t *testing.T) {
	raw := strings.Repeat("a", 100)
	p := ProbeRefreshToken(raw, nil)
	if p.Reason != "not_three_segments" {
		t.Fatalf("reason=%q", p.Reason)
	}
	if p.DotCount != 0 {
		t.Fatalf("dot_count=%d", p.DotCount)
	}
}

func TestProbeRefreshToken_ExpiredAccessUsedAsRefresh(t *testing.T) {
	s := testJWTService(t)
	s.accessExpiry = -time.Minute
	pair, _, err := s.GenerateAccessToken("+260700000000", "DE1", "de")
	if err != nil {
		t.Fatal(err)
	}
	_, verifyErr := s.VerifyToken(pair.AccessToken)
	p := ProbeRefreshToken(pair.AccessToken, verifyErr)
	if !p.LooksLikeJWT {
		t.Fatal("expected jwt")
	}
	if p.UnverifiedType != "access" {
		t.Fatalf("type=%q", p.UnverifiedType)
	}
	if p.UnverifiedEntityType != "de" {
		t.Fatalf("entity_type=%q", p.UnverifiedEntityType)
	}
	if !p.Expired {
		t.Fatal("expected expired")
	}
	if p.VerifyClass != "expired" {
		t.Fatalf("verify_class=%q", p.VerifyClass)
	}
	if p.Reason != "expired_access_used_as_refresh" {
		t.Fatalf("reason=%q", p.Reason)
	}
	if _, ok := p.LogFields()["phone"]; ok {
		t.Fatal("must not log phone")
	}
}

func TestProbeRefreshToken_ValidRefreshUnknownUntilRevoke(t *testing.T) {
	s := testJWTService(t)
	pair, _, err := s.GenerateAccessToken("+260700000000", "DE1", "de")
	if err != nil {
		t.Fatal(err)
	}
	p := ProbeRefreshToken(pair.RefreshToken, nil)
	if p.UnverifiedType != "refresh" {
		t.Fatalf("type=%q", p.UnverifiedType)
	}
	if p.Expired {
		t.Fatal("fresh refresh should not be expired")
	}
	if p.Reason != "unknown" {
		t.Fatalf("reason=%q want unknown (verify succeeded)", p.Reason)
	}
}

func TestProbeRefreshToken_DoesNotLogSecret(t *testing.T) {
	p := ProbeRefreshToken("aaa.bbb.ccc", nil)
	fields := p.LogFields()
	for k, v := range fields {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if strings.Contains(s, "aaa.bbb.ccc") {
			t.Fatalf("field %s leaked token", k)
		}
	}
}
