//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func TestVonageJWTRepository_Get_IgnoresExpiredTTLInDynamoDB(t *testing.T) {
	deleteVonageJWTCache(t)
	t.Cleanup(func() { deleteVonageJWTCache(t) })

	seedVonageJWTCache(t, "expired-jwt", time.Now().Add(-1*time.Minute))

	repo := newTestVonageJWTRepository(logrus.New())
	got, err := repo.Get(context.Background())
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got != "" {
		t.Fatalf("Get() = %q, want empty string for expired cache", got)
	}
}

func TestVonageJWTRepository_Get_ReturnsValidCachedJWT(t *testing.T) {
	deleteVonageJWTCache(t)
	t.Cleanup(func() { deleteVonageJWTCache(t) })

	seedVonageJWTCache(t, "valid-jwt", time.Now().Add(30*time.Minute))

	repo := newTestVonageJWTRepository(logrus.New())
	got, err := repo.Get(context.Background())
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got != "valid-jwt" {
		t.Fatalf("Get() = %q, want valid-jwt", got)
	}
}

func TestVonageJWTRepository_Delete_RemovesCachedJWT(t *testing.T) {
	deleteVonageJWTCache(t)
	t.Cleanup(func() { deleteVonageJWTCache(t) })

	seedVonageJWTCache(t, "to-delete", time.Now().Add(30*time.Minute))

	repo := newTestVonageJWTRepository(logrus.New())
	if err := repo.Delete(context.Background()); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}

	got, err := repo.Get(context.Background())
	if err != nil {
		t.Fatalf("Get after Delete returned error: %v", err)
	}
	if got != "" {
		t.Fatalf("Get after Delete = %q, want empty", got)
	}
}

func TestVonageService_SendWhatsAppOTP_RetriesAfter401WithDynamoDBCache(t *testing.T) {
	deleteVonageJWTCache(t)
	t.Cleanup(func() { deleteVonageJWTCache(t) })

	seedVonageJWTCache(t, "stale-jwt", time.Now().Add(30*time.Minute))

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			if auth := r.Header.Get("Authorization"); auth != "Bearer stale-jwt" {
				t.Errorf("first call Authorization = %q, want Bearer stale-jwt", auth)
			}
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"title": "Unauthorised"})
			return
		}

		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var body map[string]interface{}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["to"] != "919515365236" {
			t.Fatalf("retry to = %v, want 919515365236", body["to"])
		}
		if auth := r.Header.Get("Authorization"); auth == "Bearer stale-jwt" {
			t.Fatal("retry should not reuse stale JWT")
		}

		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"message_uuid": "retry-uuid"})
	}))
	defer server.Close()

	repo := newTestVonageJWTRepository(logrus.New())
	svc := newTestVonageService(t, repo, server.URL, server.Client())

	if err := svc.SendWhatsAppOTP(context.Background(), "+919515365236", "123456"); err != nil {
		t.Fatalf("SendWhatsAppOTP returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 Vonage calls, got %d", calls)
	}

	cached := getVonageJWTCacheToken(t)
	if cached == "" {
		t.Fatal("expected refreshed JWT to be cached in DynamoDB")
	}
	if cached == "stale-jwt" {
		t.Fatal("cached JWT should be refreshed after 401")
	}
}
