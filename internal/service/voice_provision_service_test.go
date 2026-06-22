package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirupsen/logrus"
)

// fakeProvisionStore implements provisionStore for tests.
type fakeProvisionStore struct {
	provisioned map[string]bool
}

func (f *fakeProvisionStore) IsProvisioned(_ context.Context, sub string) (bool, error) {
	return f.provisioned[sub], nil
}

func (f *fakeProvisionStore) MarkProvisioned(_ context.Context, sub string) error {
	if f.provisioned == nil {
		f.provisioned = make(map[string]bool)
	}
	f.provisioned[sub] = true
	return nil
}

func TestEnsureUserSkipsWhenProvisioned(t *testing.T) {
	store := &fakeProvisionStore{provisioned: map[string]bool{"cust_U9": true}}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer srv.Close()
	s := NewVoiceProvisionService(store, srv.URL, "appid", testRSAKeyB64(t), logrus.New())
	if err := s.EnsureUser(context.Background(), "cust_U9"); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("expected no Vonage call, got %d", calls)
	}
}

func TestEnsureUserPostsWhenNotProvisioned(t *testing.T) {
	store := &fakeProvisionStore{provisioned: map[string]bool{}}
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	s := NewVoiceProvisionService(store, srv.URL, "appid", testRSAKeyB64(t), logrus.New())
	if err := s.EnsureUser(context.Background(), "cust_U1"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/v0.3/users" {
		t.Fatalf("path = %s, want /v0.3/users", gotPath)
	}
	if !store.provisioned["cust_U1"] {
		t.Fatal("expected sub to be marked provisioned after 201")
	}
}

func TestEnsureUserTreats409AsSuccess(t *testing.T) {
	store := &fakeProvisionStore{provisioned: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()
	s := NewVoiceProvisionService(store, srv.URL, "appid", testRSAKeyB64(t), logrus.New())
	if err := s.EnsureUser(context.Background(), "cust_U2"); err != nil {
		t.Fatalf("expected nil on 409, got %v", err)
	}
	if !store.provisioned["cust_U2"] {
		t.Fatal("expected sub to be marked provisioned after 409")
	}
}

func TestEnsureUserReturnsErrorOnNon2xx(t *testing.T) {
	store := &fakeProvisionStore{provisioned: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	s := NewVoiceProvisionService(store, srv.URL, "appid", testRSAKeyB64(t), logrus.New())
	if err := s.EnsureUser(context.Background(), "cust_U3"); err == nil {
		t.Fatal("expected error on 500, got nil")
	}
}
