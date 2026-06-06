package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/sirupsen/logrus"
)

type stubVonageJWTCache struct {
	cached  string
	deleted bool
	stored  []string
}

func (s *stubVonageJWTCache) Get(context.Context) (string, error) {
	return s.cached, nil
}

func (s *stubVonageJWTCache) Store(_ context.Context, jwt string) error {
	s.stored = append(s.stored, jwt)
	s.cached = jwt
	return nil
}

func (s *stubVonageJWTCache) Delete(context.Context) error {
	s.deleted = true
	s.cached = ""
	return nil
}

func generateTestPrivateKey(t *testing.T) (string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate test RSA key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	pemBytes := pem.EncodeToMemory(block)
	b64 := base64.StdEncoding.EncodeToString(pemBytes)
	return b64, key
}

func TestGenerateVonageJWT_ClaimsAreCorrect(t *testing.T) {
	b64Key, privateKey := generateTestPrivateKey(t)
	appID := "test-app-id-123"

	svc := &VonageService{
		appID:         appID,
		privateKeyB64: b64Key,
	}

	before := time.Now().Unix()
	tokenStr, err := svc.generateJWT()
	after := time.Now().Unix()

	if err != nil {
		t.Fatalf("generateJWT returned error: %v", err)
	}
	if tokenStr == "" {
		t.Fatal("generateJWT returned empty token")
	}

	// Parse and verify with the public key
	token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			t.Fatalf("unexpected signing method: %v", token.Header["alg"])
		}
		return &privateKey.PublicKey, nil
	})
	if err != nil {
		t.Fatalf("failed to parse generated JWT: %v", err)
	}
	if !token.Valid {
		t.Fatal("generated JWT is not valid")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatal("expected MapClaims")
	}

	if claims["application_id"] != appID {
		t.Errorf("application_id: got %v, want %v", claims["application_id"], appID)
	}

	iat := int64(claims["iat"].(float64))
	exp := int64(claims["exp"].(float64))

	if iat < before || iat > after {
		t.Errorf("iat %d not in range [%d, %d]", iat, before, after)
	}
	if exp-iat != 3600 {
		t.Errorf("exp-iat: got %d, want 3600", exp-iat)
	}

	if claims["sub"] != "" {
		t.Errorf("sub: got %v, want empty string", claims["sub"])
	}
	if claims["acl"] != "" {
		t.Errorf("acl: got %v, want empty string", claims["acl"])
	}

	jti, ok := claims["jti"].(string)
	if !ok || jti == "" {
		t.Error("jti must be a non-empty string")
	}
}

func TestSendWhatsAppOTP_RetriesAfter401(t *testing.T) {
	b64Key, _ := generateTestPrivateKey(t)
	calls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"title": "Unauthorised"})
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("invalid request body: %v", err)
		}
		if payload["to"] != "919515365236" {
			t.Fatalf("to = %v, want 919515365236", payload["to"])
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	repo := &stubVonageJWTCache{cached: "stale-jwt"}
	svc := &VonageService{
		appID:         "test-app-id",
		privateKeyB64: b64Key,
		whatsAppFrom:  "15559615672",
		jwtRepo:       repo,
		httpClient:    server.Client(),
		logger:        logrus.New(),
		messagesURL:   server.URL,
	}

	if err := svc.SendWhatsAppOTP(context.Background(), "+919515365236", "123456"); err != nil {
		t.Fatalf("SendWhatsAppOTP returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 Vonage calls, got %d", calls)
	}
	if !repo.deleted {
		t.Fatal("expected stale JWT cache to be deleted after 401")
	}
	if len(repo.stored) != 1 {
		t.Fatalf("expected 1 refreshed JWT stored, got %d", len(repo.stored))
	}
	if repo.stored[0] == "stale-jwt" {
		t.Fatal("refreshed JWT should not reuse stale cached value")
	}
}

func TestPostMessageWithRetries_RetriesOnTimeout(t *testing.T) {
	b64Key, _ := generateTestPrivateKey(t)
	attempts := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= vonageMaxRetries {
			time.Sleep(vonageHTTPTimeout + 100*time.Millisecond)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	svc := &VonageService{
		appID:         "test-app-id",
		privateKeyB64: b64Key,
		whatsAppFrom:  "15559615672",
		jwtRepo:       &stubVonageJWTCache{cached: "valid-jwt"},
		httpClient:    &http.Client{Timeout: vonageHTTPTimeout},
		logger:        logrus.New(),
		messagesURL:   server.URL,
	}

	body := []byte(`{"to":"919515365236"}`)
	statusCode, _, err := svc.postMessageWithRetries(context.Background(), "valid-jwt", body)
	if err != nil {
		t.Fatalf("postMessageWithRetries returned error: %v", err)
	}
	if statusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", statusCode, http.StatusAccepted)
	}
	if attempts != vonageMaxRetries+1 {
		t.Fatalf("attempts = %d, want %d", attempts, vonageMaxRetries+1)
	}
}

func TestPostMessageWithRetries_RetriesOn5xx(t *testing.T) {
	b64Key, _ := generateTestPrivateKey(t)
	attempts := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	svc := &VonageService{
		appID:         "test-app-id",
		privateKeyB64: b64Key,
		jwtRepo:       &stubVonageJWTCache{cached: "valid-jwt"},
		httpClient:    server.Client(),
		logger:        logrus.New(),
		messagesURL:   server.URL,
	}

	body := []byte(`{"to":"919515365236"}`)
	statusCode, _, err := svc.postMessageWithRetries(context.Background(), "valid-jwt", body)
	if err != nil {
		t.Fatalf("postMessageWithRetries returned error: %v", err)
	}
	if statusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", statusCode, http.StatusAccepted)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestGenerateVonageJWT_PartsCount(t *testing.T) {
	b64Key, _ := generateTestPrivateKey(t)
	svc := &VonageService{appID: "x", privateKeyB64: b64Key}

	tokenStr, err := svc.generateJWT()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 3 {
		t.Errorf("JWT must have 3 parts, got %d", len(parts))
	}
}
