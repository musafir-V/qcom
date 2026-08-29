package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

// dropDeadlineConfigStore is the slice of DropDeadlineConfigRepository this
// handler needs, declared as an interface so tests can inject a stub.
type dropDeadlineConfigStore interface {
	Get(ctx context.Context) (*models.DropDeadlineConfig, error)
	Put(ctx context.Context, cfg *models.DropDeadlineConfig) error
}

// AdminDropDeadlineHandlers powers the drop-deadline countdown admin API.
// Mount behind RequireAdminAuth (parent router applies auth).
type AdminDropDeadlineHandlers struct {
	repo   dropDeadlineConfigStore
	logger *logrus.Logger
}

func NewAdminDropDeadlineHandlers(repo *repository.DropDeadlineConfigRepository, logger *logrus.Logger) *AdminDropDeadlineHandlers {
	return &AdminDropDeadlineHandlers{repo: repo, logger: logger}
}

// newAdminDropDeadlineHandlers is the test-friendly constructor.
func newAdminDropDeadlineHandlers(repo dropDeadlineConfigStore, logger *logrus.Logger) *AdminDropDeadlineHandlers {
	return &AdminDropDeadlineHandlers{repo: repo, logger: logger}
}

type dropDeadlineResponse struct {
	MinutesPerKm float64 `json:"minutes_per_km"`
	ExtraMinutes float64 `json:"extra_minutes"`
}

// GetConfig handles GET /config/drop-deadline → effective values (never 0 x).
func (h *AdminDropDeadlineHandlers) GetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.repo.Get(r.Context())
	if err != nil {
		h.logger.WithError(err).Error("drop deadline: get failed")
		h.respondWithError(w, http.StatusInternalServerError, "CONFIG_FETCH_FAILED", "Failed to load drop-deadline config")
		return
	}
	h.respondWithJSON(w, http.StatusOK, dropDeadlineResponse{
		MinutesPerKm: cfg.EffectiveMinutesPerKm(),
		ExtraMinutes: cfg.EffectiveExtraMinutes(),
	})
}

// PatchConfig handles PATCH /config/drop-deadline. Both fields required;
// minutes_per_km must be > 0; extra_minutes must be >= 0.
func (h *AdminDropDeadlineHandlers) PatchConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MinutesPerKm *float64 `json:"minutes_per_km"`
		ExtraMinutes *float64 `json:"extra_minutes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.MinutesPerKm == nil || req.ExtraMinutes == nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "minutes_per_km and extra_minutes are required")
		return
	}
	if *req.MinutesPerKm <= 0 {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "minutes_per_km must be greater than 0")
		return
	}
	if *req.ExtraMinutes < 0 {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "extra_minutes must be greater than or equal to 0")
		return
	}
	cfg := &models.DropDeadlineConfig{
		MinutesPerKm: *req.MinutesPerKm,
		ExtraMinutes: *req.ExtraMinutes,
	}
	if err := h.repo.Put(r.Context(), cfg); err != nil {
		h.logger.WithError(err).Error("drop deadline: put failed")
		h.respondWithError(w, http.StatusInternalServerError, "CONFIG_UPDATE_FAILED", "Failed to update drop-deadline config")
		return
	}
	h.respondWithJSON(w, http.StatusOK, dropDeadlineResponse{
		MinutesPerKm: cfg.MinutesPerKm,
		ExtraMinutes: cfg.ExtraMinutes,
	})
}

func (h *AdminDropDeadlineHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (h *AdminDropDeadlineHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{Error: ErrorDetail{Code: code, Message: message}})
}
