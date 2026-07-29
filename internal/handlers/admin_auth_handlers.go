package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type adminUserService interface {
	Authenticate(ctx context.Context, username, password string) (*models.AdminUser, error)
	Get(ctx context.Context, username string) (*models.AdminUser, error)
	List(ctx context.Context) ([]*models.AdminUser, error)
	CreateUser(ctx context.Context, username, password, name string) (*models.AdminUser, error)
	ChangePassword(ctx context.Context, username, newPassword string) error
}

type adminTokenIssuer interface {
	GenerateAdminToken(username string) (string, int64, error)
}

type AdminAuthHandlers struct {
	users  adminUserService
	tokens adminTokenIssuer
	logger *logrus.Logger
}

func NewAdminAuthHandlers(users adminUserService, tokens adminTokenIssuer, logger *logrus.Logger) *AdminAuthHandlers {
	return &AdminAuthHandlers{users: users, tokens: tokens, logger: logger}
}

type adminUserView struct {
	Username string `json:"username"`
	Name     string `json:"name"`
}

type loginResponse struct {
	Token     string        `json:"token"`
	TokenType string        `json:"token_type"`
	ExpiresIn int64         `json:"expires_in"`
	User      adminUserView `json:"user"`
}

// Login authenticates a username/password pair and returns a bearer token.
func (h *AdminAuthHandlers) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		adminAuthRespondError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if strings.TrimSpace(req.Username) == "" || req.Password == "" {
		adminAuthRespondError(w, http.StatusBadRequest, "MISSING_FIELD", "username and password are required")
		return
	}

	user, err := h.users.Authenticate(r.Context(), req.Username, req.Password)
	if err != nil {
		if errors.Is(err, service.ErrInvalidCredentials) {
			adminAuthRespondError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Invalid username or password")
			return
		}
		h.logger.WithError(err).Error("admin login: authenticate failed")
		adminAuthRespondError(w, http.StatusInternalServerError, "LOGIN_FAILED", "Login failed")
		return
	}

	token, expiresIn, err := h.tokens.GenerateAdminToken(user.Username)
	if err != nil {
		h.logger.WithError(err).Error("admin login: token generation failed")
		adminAuthRespondError(w, http.StatusInternalServerError, "LOGIN_FAILED", "Login failed")
		return
	}

	adminAuthRespondJSON(w, http.StatusOK, loginResponse{
		Token:     token,
		TokenType: "Bearer",
		ExpiresIn: expiresIn,
		User:      adminUserView{Username: user.Username, Name: user.Name},
	})
}

// Me returns the currently authenticated admin user.
func (h *AdminAuthHandlers) Me(w http.ResponseWriter, r *http.Request) {
	username := entityIDFrom(r)
	user, err := h.users.Get(r.Context(), username)
	if err != nil {
		h.logger.WithError(err).Error("admin me: lookup failed")
		adminAuthRespondError(w, http.StatusInternalServerError, "LOOKUP_FAILED", "Failed to load account")
		return
	}
	if user == nil {
		adminAuthRespondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Account no longer exists")
		return
	}
	adminAuthRespondJSON(w, http.StatusOK, adminUserView{Username: user.Username, Name: user.Name})
}

// ListUsers returns all admin accounts (without password hashes).
func (h *AdminAuthHandlers) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.users.List(r.Context())
	if err != nil {
		h.logger.WithError(err).Error("admin users: list failed")
		adminAuthRespondError(w, http.StatusInternalServerError, "USERS_LIST_FAILED", "Failed to list users")
		return
	}
	adminAuthRespondJSON(w, http.StatusOK, users)
}

// CreateUser creates a new admin account.
func (h *AdminAuthHandlers) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		adminAuthRespondError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	user, err := h.users.CreateUser(r.Context(), req.Username, req.Password, req.Name)
	if err != nil {
		h.handleUserWriteError(w, err)
		return
	}
	adminAuthRespondJSON(w, http.StatusCreated, adminUserView{Username: user.Username, Name: user.Name})
}

// ChangePassword updates the password for the user named in the path.
func (h *AdminAuthHandlers) ChangePassword(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(mux.Vars(r)["username"])
	if username == "" {
		adminAuthRespondError(w, http.StatusBadRequest, "MISSING_PARAM", "username is required")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		adminAuthRespondError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if err := h.users.ChangePassword(r.Context(), username, req.Password); err != nil {
		h.handleUserWriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminAuthHandlers) handleUserWriteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, repository.ErrAdminUserExists):
		adminAuthRespondError(w, http.StatusConflict, "USER_EXISTS", "An admin with that username already exists")
	case errors.Is(err, service.ErrWeakPassword):
		adminAuthRespondError(w, http.StatusBadRequest, "WEAK_PASSWORD", err.Error())
	case errors.Is(err, service.ErrInvalidUsername):
		adminAuthRespondError(w, http.StatusBadRequest, "INVALID_USERNAME", err.Error())
	default:
		h.logger.WithError(err).Error("admin users: write failed")
		adminAuthRespondError(w, http.StatusInternalServerError, "USERS_WRITE_FAILED", "Failed to save user")
	}
}

func adminAuthRespondJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func adminAuthRespondError(w http.ResponseWriter, status int, code, message string) {
	adminAuthRespondJSON(w, status, ErrorResponse{Error: ErrorDetail{Code: code, Message: message}})
}
