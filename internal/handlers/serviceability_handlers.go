package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type ServiceabilityHandlers struct {
	serviceabilityService *service.ServiceabilityService
	logger                *logrus.Logger
}

func NewServiceabilityHandlers(serviceabilityService *service.ServiceabilityService, logger *logrus.Logger) *ServiceabilityHandlers {
	return &ServiceabilityHandlers{
		serviceabilityService: serviceabilityService,
		logger:                logger,
	}
}

// ServiceabilityRequest uses pointers so a missing field is distinguishable
// from a legitimate 0,0 coordinate.
type ServiceabilityRequest struct {
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}

func (h *ServiceabilityHandlers) CheckServiceability(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value("entity_id").(string)
	if !ok || userID == "" {
		respondWithError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Entity ID not found in token")
		return
	}

	var req ServiceabilityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	if req.Latitude == nil {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "latitude is required")
		return
	}
	if req.Longitude == nil {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "longitude is required")
		return
	}
	if *req.Latitude < -90 || *req.Latitude > 90 {
		respondWithError(w, http.StatusBadRequest, "INVALID_COORDINATES", "Latitude must be between -90 and 90")
		return
	}
	if *req.Longitude < -180 || *req.Longitude > 180 {
		respondWithError(w, http.StatusBadRequest, "INVALID_COORDINATES", "Longitude must be between -180 and 180")
		return
	}

	result, err := h.serviceabilityService.CheckServiceability(r.Context(), userID, *req.Latitude, *req.Longitude)
	if err != nil {
		h.logger.WithError(err).Error("Failed to check serviceability")
		respondWithError(w, http.StatusInternalServerError, "SERVICEABILITY_CHECK_FAILED", "Failed to check serviceability")
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]interface{}{"data": result})
}
