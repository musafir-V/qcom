package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

// tripReachedConfigStore is the slice of TripReachedConfigRepository this
// handler needs, declared as an interface so tests can inject a stub.
type tripReachedConfigStore interface {
	Get(ctx context.Context) (*models.TripReachedConfig, error)
	Put(ctx context.Context, cfg *models.TripReachedConfig) error
}

// AdminTripReachedHandlers powers the drop-reached geofence admin API.
// Mount behind RequireAdminAuth (parent router applies auth).
type AdminTripReachedHandlers struct {
	repo   tripReachedConfigStore
	logger *logrus.Logger
}

func NewAdminTripReachedHandlers(repo *repository.TripReachedConfigRepository, logger *logrus.Logger) *AdminTripReachedHandlers {
	return &AdminTripReachedHandlers{repo: repo, logger: logger}
}

// newAdminTripReachedHandlers is the test-friendly constructor.
func newAdminTripReachedHandlers(repo tripReachedConfigStore, logger *logrus.Logger) *AdminTripReachedHandlers {
	return &AdminTripReachedHandlers{repo: repo, logger: logger}
}

type tripReachedResponse struct {
	RadiusMeters                 float64 `json:"radius_meters"`
	RequireReachedBeforeComplete bool    `json:"require_reached_before_complete"`
}

// GetConfig handles GET /config/drop-reached → effective values (never 0 radius).
func (h *AdminTripReachedHandlers) GetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.repo.Get(r.Context())
	if err != nil {
		h.logger.WithError(err).Error("trip reached: get failed")
		h.respondWithError(w, http.StatusInternalServerError, "CONFIG_FETCH_FAILED", "Failed to load drop-reached config")
		return
	}
	h.respondWithJSON(w, http.StatusOK, tripReachedResponse{
		RadiusMeters:                 cfg.EffectiveRadiusMeters(),
		RequireReachedBeforeComplete: cfg.RequireReached(),
	})
}

// PatchConfig handles PATCH /config/drop-reached. Both fields required; radius must be > 0.
func (h *AdminTripReachedHandlers) PatchConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RadiusMeters                 *float64 `json:"radius_meters"`
		RequireReachedBeforeComplete *bool    `json:"require_reached_before_complete"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RadiusMeters == nil || req.RequireReachedBeforeComplete == nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "radius_meters and require_reached_before_complete are required")
		return
	}
	if *req.RadiusMeters <= 0 {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "radius_meters must be greater than 0")
		return
	}
	cfg := &models.TripReachedConfig{
		RadiusMeters:                 *req.RadiusMeters,
		RequireReachedBeforeComplete: *req.RequireReachedBeforeComplete,
	}
	if err := h.repo.Put(r.Context(), cfg); err != nil {
		h.logger.WithError(err).Error("trip reached: put failed")
		h.respondWithError(w, http.StatusInternalServerError, "CONFIG_UPDATE_FAILED", "Failed to update drop-reached config")
		return
	}
	h.respondWithJSON(w, http.StatusOK, tripReachedResponse{
		RadiusMeters:                 cfg.RadiusMeters,
		RequireReachedBeforeComplete: cfg.RequireReachedBeforeComplete,
	})
}

func (h *AdminTripReachedHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (h *AdminTripReachedHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{Error: ErrorDetail{Code: code, Message: message}})
}
