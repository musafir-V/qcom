package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type AuthMiddleware struct {
	jwtService *service.JWTService
	logger     *logrus.Logger
}

func NewAuthMiddleware(jwtService *service.JWTService, logger *logrus.Logger) *AuthMiddleware {
	return &AuthMiddleware{
		jwtService: jwtService,
		logger:     logger,
	}
}

func (m *AuthMiddleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			m.respondUnauthorized(w, "Missing authorization header")
			return
		}

		// Extract token from "Bearer <token>"
		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			m.respondUnauthorized(w, "Invalid authorization header format")
			return
		}

		tokenString := parts[1]

		// Verify token
		claims, err := m.jwtService.VerifyToken(tokenString)
		if err != nil {
			m.logger.WithError(err).Debug("Token verification failed")
			m.respondUnauthorized(w, "Invalid or expired token")
			return
		}

		// Check token type
		if claims.Type != "access" {
			m.respondUnauthorized(w, "Invalid token type")
			return
		}

		// Add claims to context
		ctx := context.WithValue(r.Context(), "claims", claims)
		ctx = context.WithValue(ctx, "phone", claims.Phone)
		ctx = context.WithValue(ctx, "entity_id", claims.EntityID)
		ctx = context.WithValue(ctx, "entity_type", claims.EntityType)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *AuthMiddleware) RequireDEAuth(next http.Handler) http.Handler {
	return m.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entityType, _ := r.Context().Value("entity_type").(string)
		if entityType != "de" {
			m.respondForbidden(w, "This endpoint requires DE authentication")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (m *AuthMiddleware) respondUnauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(`{"error":{"code":"UNAUTHORIZED","message":"` + message + `"}}`))
}

func (m *AuthMiddleware) respondForbidden(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	w.Write([]byte(`{"error":{"code":"FORBIDDEN","message":"` + message + `"}}`))
}
