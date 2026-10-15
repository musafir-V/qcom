package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/service"
)

func testRefreshJWT(t *testing.T) *service.JWTService {
	t.Helper()
	svc, err := service.NewJWTService(&config.JWTConfig{
		SecretKey:      strings.Repeat("r", 32),
		AccessExpiry:   15 * time.Minute,
		RefreshExpiry:  time.Hour,
		AbsoluteExpiry: 24 * time.Hour,
	}, testAuthLogger())
	if err != nil {
		t.Fatalf("NewJWTService: %v", err)
	}
	return svc
}

func signTestRefresh(t *testing.T, secret, tokenType string, absExp int64, expiresAt time.Time) string {
	t.Helper()
	jti := uuid.New().String()
	claims := &service.Claims{
		Phone:      "+260971000001",
		EntityID:   "user-1",
		EntityType: "customer",
		Type:       tokenType,
		JTI:        jti,
		AbsExp:     absExp,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-1",
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			ID:        jti,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func postRefresh(t *testing.T, h *AuthHandlers, raw string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"refresh_token":""}`
	if raw != "" {
		body = `{"refresh_token":"` + raw + `"}`
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.RefreshToken(rec, req)
	return rec
}

func assertRefreshRevoked(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	got := decodeAuthError(t, rec)
	if got.Error.Code != "TOKEN_REVOKED" {
		t.Fatalf("code = %q, want TOKEN_REVOKED body=%s", got.Error.Code, rec.Body.String())
	}
	if got.Error.Message != "Refresh token has been revoked" {
		t.Fatalf("message = %q", got.Error.Message)
	}
}

func TestRefreshToken_ExpiredJWT_ReturnsRevoked(t *testing.T) {
	jwtSvc := testRefreshJWT(t)
	raw := signTestRefresh(t, strings.Repeat("r", 32), "refresh", 0, time.Now().Add(-time.Minute))
	h := &AuthHandlers{jwtService: jwtSvc, logger: testAuthLogger()}

	assertRefreshRevoked(t, postRefresh(t, h, raw))
}

func TestRefreshToken_InvalidJWT_ReturnsRevoked(t *testing.T) {
	jwtSvc := testRefreshJWT(t)
	raw := signTestRefresh(t, strings.Repeat("x", 32), "refresh", 0, time.Now().Add(time.Hour))
	h := &AuthHandlers{jwtService: jwtSvc, logger: testAuthLogger()}

	assertRefreshRevoked(t, postRefresh(t, h, raw))
}

func TestRefreshToken_WrongType_ReturnsRevoked(t *testing.T) {
	jwtSvc := testRefreshJWT(t)
	pair, _, err := jwtSvc.GenerateAccessToken("+260971000001", "user-1", "customer")
	if err != nil {
		t.Fatal(err)
	}
	h := &AuthHandlers{jwtService: jwtSvc, logger: testAuthLogger()}

	assertRefreshRevoked(t, postRefresh(t, h, pair.AccessToken))
}

type rotateMintFunc func(ctx context.Context, oldJTI, entityID, entityType, phone string, tokenExpiresAt time.Time, mint service.MintFunc) (*models.TokenPair, error)

type fakeRefreshAPI struct {
	rotate rotateMintFunc
}

func (f fakeRefreshAPI) Store(context.Context, string, string, string, string, string, time.Time) error {
	return nil
}
func (f fakeRefreshAPI) Get(context.Context, string) (*models.RefreshTokenData, error) {
	return nil, nil
}
func (f fakeRefreshAPI) Revoke(context.Context, string) error { return nil }
func (f fakeRefreshAPI) RevokeFamily(context.Context, string) error {
	return nil
}
func (f fakeRefreshAPI) RevokeAllForEntity(context.Context, string) error { return nil }
func (f fakeRefreshAPI) Rotate(ctx context.Context, oldJTI, entityID, entityType, phone string, tokenExpiresAt time.Time, mint service.MintFunc) (*models.TokenPair, error) {
	return f.rotate(ctx, oldJTI, entityID, entityType, phone, tokenExpiresAt, mint)
}

func TestRefreshToken_AbsoluteSessionExpired_ReturnsRevoked(t *testing.T) {
	jwtSvc := testRefreshJWT(t)
	raw := signTestRefresh(t, strings.Repeat("r", 32), "refresh", time.Now().Add(-time.Minute).Unix(), time.Now().Add(time.Hour))
	h := &AuthHandlers{
		jwtService: jwtSvc,
		refreshTokenService: fakeRefreshAPI{
			rotate: func(ctx context.Context, oldJTI, entityID, entityType, phone string, tokenExpiresAt time.Time, mint service.MintFunc) (*models.TokenPair, error) {
				_, _, _, _, err := mint("family-abs")
				return nil, err
			},
		},
		logger: testAuthLogger(),
	}

	assertRefreshRevoked(t, postRefresh(t, h, raw))
}

func TestRefreshToken_MissingToken_StaysMissing(t *testing.T) {
	h := &AuthHandlers{logger: testAuthLogger()}
	rec := postRefresh(t, h, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	got := decodeAuthError(t, rec)
	if got.Error.Code != "MISSING_TOKEN" {
		t.Fatalf("code = %q, want MISSING_TOKEN", got.Error.Code)
	}
}
