package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/qcom/qcom/internal/repository"

	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

// ErrInvalidCredentials is returned when a username/password pair does not
// match an active admin account.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrWeakPassword is returned when a new password is too short.
var ErrWeakPassword = errors.New("password must be at least 8 characters")

// ErrInvalidUsername is returned when a username is empty after normalization.
var ErrInvalidUsername = errors.New("username is required")

// minPasswordLen is the minimum admin password length.
const minPasswordLen = 8

// dummyHash is a precomputed bcrypt hash compared against when a user is not
// found, so login timing does not reveal whether a username exists.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("timing-equalizer"), bcrypt.DefaultCost)

type adminUserRepository interface {
	GetByUsername(ctx context.Context, username string) (*models.AdminUser, error)
	Create(ctx context.Context, user *models.AdminUser) error
	Put(ctx context.Context, user *models.AdminUser) error
	ListAll(ctx context.Context) ([]*models.AdminUser, error)
}

type AdminUserService struct {
	repo   adminUserRepository
	logger *logrus.Logger
}

func NewAdminUserService(repo adminUserRepository, logger *logrus.Logger) *AdminUserService {
	return &AdminUserService{repo: repo, logger: logger}
}

// NormalizeUsername lowercases and trims a username so lookups are stable.
func NormalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// Authenticate verifies a username/password pair and returns the matching
// active user, or ErrInvalidCredentials.
func (s *AdminUserService) Authenticate(ctx context.Context, username, password string) (*models.AdminUser, error) {
	username = NormalizeUsername(username)
	user, err := s.repo.GetByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	if user == nil || user.Disabled {
		// Equalize timing against the found-user path.
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}
	return user, nil
}

// Get returns a single admin user by username, or nil if not found.
func (s *AdminUserService) Get(ctx context.Context, username string) (*models.AdminUser, error) {
	return s.repo.GetByUsername(ctx, NormalizeUsername(username))
}

// List returns all admin users (password hashes are omitted from JSON output).
func (s *AdminUserService) List(ctx context.Context) ([]*models.AdminUser, error) {
	return s.repo.ListAll(ctx)
}

// CreateUser creates a new admin account with a bcrypt-hashed password.
func (s *AdminUserService) CreateUser(ctx context.Context, username, password, name string) (*models.AdminUser, error) {
	username = NormalizeUsername(username)
	if username == "" {
		return nil, ErrInvalidUsername
	}
	if len(password) < minPasswordLen {
		return nil, ErrWeakPassword
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	user := &models.AdminUser{
		Username:     username,
		Name:         strings.TrimSpace(name),
		PasswordHash: string(hash),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.repo.Create(ctx, user); err != nil {
		return nil, err
	}
	return user, nil
}

// ChangePassword sets a new bcrypt-hashed password for an existing user.
func (s *AdminUserService) ChangePassword(ctx context.Context, username, newPassword string) error {
	username = NormalizeUsername(username)
	if len(newPassword) < minPasswordLen {
		return ErrWeakPassword
	}
	user, err := s.repo.GetByUsername(ctx, username)
	if err != nil {
		return err
	}
	if user == nil {
		return ErrInvalidUsername
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	user.PasswordHash = string(hash)
	user.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return s.repo.Put(ctx, user)
}

// Bootstrap creates the initial admin account from environment-provided
// credentials if it does not already exist. It is idempotent and safe to call
// on every boot. A no-op when either value is empty.
func (s *AdminUserService) Bootstrap(ctx context.Context, username, password, name string) error {
	username = NormalizeUsername(username)
	if username == "" || password == "" {
		return nil
	}
	existing, err := s.repo.GetByUsername(ctx, username)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}
	if name == "" {
		name = username
	}
	if _, err := s.CreateUser(ctx, username, password, name); err != nil {
		if errors.Is(err, repository.ErrAdminUserExists) {
			return nil
		}
		return err
	}
	s.logger.WithField("username", username).Info("Bootstrapped initial admin user")
	return nil
}
