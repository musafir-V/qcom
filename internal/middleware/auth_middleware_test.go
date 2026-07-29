package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/qcom/qcom/internal/config"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

const testSecret = "test-secret-key-that-is-long-enough-32"

func newTestMiddleware(t *testing.T) *AuthMiddleware {
	t.Helper()
	logger := logrus.New()
	logger.SetOutput(discardWriter{})
	jwtService, err := service.NewJWTService(&config.JWTConfig{
		SecretKey:     testSecret,
		AccessExpiry:  time.Hour,
		RefreshExpiry: 24 * time.Hour,
	}, logger)
	if err != nil {
		t.Fatalf("NewJWTService: %v", err)
	}
	return NewAuthMiddleware(jwtService, logger)
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// signToken mints a token directly so tests can control claims the public
// JWTService API does not expose (token type, entity type, expiry).
func signToken(t *testing.T, claims *service.Claims) string {
	t.Helper()
	if claims.ExpiresAt == nil {
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func accessToken(t *testing.T, entityType string) string {
	t.Helper()
	return signToken(t, &service.Claims{
		Phone:      "0971234567",
		EntityID:   "ent-1",
		EntityType: entityType,
		Type:       "access",
	})
}

// okHandler records whether the chain reached the protected handler and what
// the middleware put on the request context.
type okHandler struct {
	called     bool
	entityType string
	entityID   string
	phone      string
}

func (h *okHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.called = true
	h.entityType, _ = r.Context().Value("entity_type").(string)
	h.entityID, _ = r.Context().Value("entity_id").(string)
	h.phone, _ = r.Context().Value("phone").(string)
	w.WriteHeader(http.StatusOK)
}

func TestRequireAuth_Rejects(t *testing.T) {
	m := newTestMiddleware(t)

	expired := signToken(t, &service.Claims{
		EntityType: "customer",
		Type:       "access",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
		},
	})
	wrongSecret, err := jwt.NewWithClaims(jwt.SigningMethodHS256, &service.Claims{
		Type: "access",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}).SignedString([]byte("another-secret-key-long-enough-here"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	cases := []struct {
		name   string
		header string
	}{
		{"missing header", ""},
		{"no bearer prefix", accessToken(t, "customer")},
		{"wrong scheme", "Token " + accessToken(t, "customer")},
		{"too many parts", "Bearer a b"},
		{"garbage token", "Bearer not-a-jwt"},
		{"expired token", "Bearer " + expired},
		{"foreign signature", "Bearer " + wrongSecret},
		{"refresh token", "Bearer " + signToken(t, &service.Claims{Type: "refresh", EntityType: "customer"})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next := &okHandler{}
			req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()

			m.RequireAuth(next).ServeHTTP(rec, req)

			if next.called {
				t.Fatal("expected next handler not to be called")
			}
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("content-type = %q", ct)
			}
			assertErrorCode(t, rec, "UNAUTHORIZED")
		})
	}
}

func TestRequireAuth_PopulatesContext(t *testing.T) {
	m := newTestMiddleware(t)
	next := &okHandler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken(t, "de"))
	rec := httptest.NewRecorder()

	m.RequireAuth(next).ServeHTTP(rec, req)

	if !next.called {
		t.Fatal("expected next handler to be called")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if next.entityType != "de" || next.entityID != "ent-1" || next.phone != "0971234567" {
		t.Fatalf("context = %+v", next)
	}
}

func TestRequireEntityTypeAuth(t *testing.T) {
	m := newTestMiddleware(t)

	cases := []struct {
		name    string
		wrap    func(http.Handler) http.Handler
		allowed string
		denied  string
	}{
		{"de", m.RequireDEAuth, "de", "customer"},
		{"customer", m.RequireCustomerAuth, "customer", "de"},
		{"admin", m.RequireAdminAuth, "admin", "customer"},
	}

	for _, tc := range cases {
		t.Run(tc.name+" allowed", func(t *testing.T) {
			next := &okHandler{}
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header.Set("Authorization", "Bearer "+accessToken(t, tc.allowed))
			rec := httptest.NewRecorder()

			tc.wrap(next).ServeHTTP(rec, req)

			if !next.called {
				t.Fatalf("expected next handler to be called for %s", tc.allowed)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
		})

		t.Run(tc.name+" denied", func(t *testing.T) {
			next := &okHandler{}
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header.Set("Authorization", "Bearer "+accessToken(t, tc.denied))
			rec := httptest.NewRecorder()

			tc.wrap(next).ServeHTTP(rec, req)

			if next.called {
				t.Fatalf("expected %s token to be rejected", tc.denied)
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
			}
			assertErrorCode(t, rec, "FORBIDDEN")
		})
	}
}

func TestRequireAuthOrGuest(t *testing.T) {
	m := newTestMiddleware(t)

	t.Run("guest header bypasses auth", func(t *testing.T) {
		for _, value := range []string{"guest", "GUEST"} {
			next := &okHandler{}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/home", nil)
			req.Header.Set(HeaderUserCategory, value)
			rec := httptest.NewRecorder()

			m.RequireAuthOrGuest(next).ServeHTTP(rec, req)

			if !next.called {
				t.Fatalf("%q: expected next handler to be called", value)
			}
			if next.entityType != EntityTypeGuest || next.entityID != "" {
				t.Fatalf("%q: context = %+v", value, next)
			}
		}
	})

	t.Run("non-guest requires token", func(t *testing.T) {
		next := &okHandler{}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/home", nil)
		req.Header.Set(HeaderUserCategory, "customer")
		rec := httptest.NewRecorder()

		m.RequireAuthOrGuest(next).ServeHTTP(rec, req)

		if next.called {
			t.Fatal("expected next handler not to be called")
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("valid token without guest header", func(t *testing.T) {
		next := &okHandler{}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/home", nil)
		req.Header.Set("Authorization", "Bearer "+accessToken(t, "customer"))
		rec := httptest.NewRecorder()

		m.RequireAuthOrGuest(next).ServeHTTP(rec, req)

		if !next.called {
			t.Fatal("expected next handler to be called")
		}
		if next.entityType != "customer" {
			t.Fatalf("entity_type = %q", next.entityType)
		}
	})
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not valid json: %v", rec.Body.String(), err)
	}
	if body.Error.Code != want {
		t.Fatalf("error code = %q, want %q", body.Error.Code, want)
	}
	if body.Error.Message == "" {
		t.Fatal("expected a non-empty error message")
	}
}
