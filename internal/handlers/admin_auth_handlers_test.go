package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type fakeAdminUserService struct {
	authUser    *models.AdminUser
	authErr     error
	createUser  *models.AdminUser
	createErr   error
	getUser     *models.AdminUser
	lastCreated struct{ username, password, name string }
}

func (f *fakeAdminUserService) Authenticate(_ context.Context, _, _ string) (*models.AdminUser, error) {
	return f.authUser, f.authErr
}
func (f *fakeAdminUserService) Get(_ context.Context, _ string) (*models.AdminUser, error) {
	return f.getUser, nil
}
func (f *fakeAdminUserService) List(_ context.Context) ([]*models.AdminUser, error) {
	return nil, nil
}
func (f *fakeAdminUserService) CreateUser(_ context.Context, username, password, name string) (*models.AdminUser, error) {
	f.lastCreated.username, f.lastCreated.password, f.lastCreated.name = username, password, name
	return f.createUser, f.createErr
}
func (f *fakeAdminUserService) ChangePassword(_ context.Context, _, _ string) error { return nil }

type fakeTokenIssuer struct{ token string }

func (f *fakeTokenIssuer) GenerateAdminToken(_ string) (string, int64, error) {
	return f.token, 3600, nil
}

func newAdminAuthHandlers(svc adminUserService, tok adminTokenIssuer) *AdminAuthHandlers {
	logger := logrus.New()
	logger.SetOutput(logrus.New().Out)
	return NewAdminAuthHandlers(svc, tok, logger)
}

func TestAdminLogin_Success(t *testing.T) {
	svc := &fakeAdminUserService{authUser: &models.AdminUser{Username: "ops", Name: "Ops Lead"}}
	h := newAdminAuthHandlers(svc, &fakeTokenIssuer{token: "tok-123"})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/login", strings.NewReader(`{"username":"ops","password":"secret123"}`))
	rec := httptest.NewRecorder()
	h.Login(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var resp loginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Token != "tok-123" {
		t.Fatalf("expected token tok-123, got %q", resp.Token)
	}
	if resp.User.Username != "ops" || resp.User.Name != "Ops Lead" {
		t.Fatalf("unexpected user view: %+v", resp.User)
	}
}

func TestAdminLogin_InvalidCredentials(t *testing.T) {
	svc := &fakeAdminUserService{authErr: service.ErrInvalidCredentials}
	h := newAdminAuthHandlers(svc, &fakeTokenIssuer{token: "tok"})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/login", strings.NewReader(`{"username":"ops","password":"wrong"}`))
	rec := httptest.NewRecorder()
	h.Login(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestAdminLogin_MissingFields(t *testing.T) {
	h := newAdminAuthHandlers(&fakeAdminUserService{}, &fakeTokenIssuer{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/login", strings.NewReader(`{"username":"","password":""}`))
	rec := httptest.NewRecorder()
	h.Login(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestAdminCreateUser_Conflict(t *testing.T) {
	svc := &fakeAdminUserService{createErr: repository.ErrAdminUserExists}
	h := newAdminAuthHandlers(svc, &fakeTokenIssuer{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", strings.NewReader(`{"username":"ops","password":"secret123","name":"Ops"}`))
	rec := httptest.NewRecorder()
	h.CreateUser(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestAdminCreateUser_WeakPassword(t *testing.T) {
	svc := &fakeAdminUserService{createErr: service.ErrWeakPassword}
	h := newAdminAuthHandlers(svc, &fakeTokenIssuer{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", strings.NewReader(`{"username":"ops","password":"x","name":"Ops"}`))
	rec := httptest.NewRecorder()
	h.CreateUser(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}
}
