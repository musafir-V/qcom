package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"

	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type distanceComputer interface {
	Compute(ctx context.Context, originLat, originLng, destLat, destLng float64, method string) (*service.ComputeDistanceResult, error)
}

type DistanceHandlers struct {
	computer distanceComputer
	logger   *logrus.Logger
}

func NewDistanceHandlers(computer distanceComputer, logger *logrus.Logger) *DistanceHandlers {
	return &DistanceHandlers{computer: computer, logger: logger}
}

type latLngRequest struct {
	Lat *float64 `json:"lat"`
	Lng *float64 `json:"lng"`
}

type computeDistanceRequest struct {
	Origin      *latLngRequest `json:"origin"`
	Destination *latLngRequest `json:"destination"`
	Method      string         `json:"method"`
}

// ComputeDistance handles POST /internal/v1/distance (private network only).
func (h *DistanceHandlers) ComputeDistance(w http.ResponseWriter, r *http.Request) {
	var req computeDistanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	originLat, originLng, ok := validateLatLng(w, req.Origin, "origin")
	if !ok {
		return
	}
	destLat, destLng, ok := validateLatLng(w, req.Destination, "destination")
	if !ok {
		return
	}

	result, err := h.computer.Compute(r.Context(), originLat, originLng, destLat, destLng, req.Method)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidDistanceMethod):
			respondWithError(w, http.StatusBadRequest, "INVALID_METHOD", "method must be haversine, google, or both")
		case errors.Is(err, service.ErrNoRoute):
			respondWithError(w, http.StatusBadRequest, "NO_ROUTE", "No drivable route between points")
		default:
			h.logger.WithError(err).Warn("Distance compute failed")
			respondWithError(w, http.StatusBadGateway, "UPSTREAM", "Failed to compute Google driving distance")
		}
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]interface{}{"data": result})
}

func validateLatLng(w http.ResponseWriter, point *latLngRequest, field string) (float64, float64, bool) {
	if point == nil {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", field+" is required")
		return 0, 0, false
	}
	if point.Lat == nil {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", field+".lat is required")
		return 0, 0, false
	}
	if point.Lng == nil {
		respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", field+".lng is required")
		return 0, 0, false
	}
	lat, lng := *point.Lat, *point.Lng
	if math.IsNaN(lat) || math.IsInf(lat, 0) || math.IsNaN(lng) || math.IsInf(lng, 0) {
		respondWithError(w, http.StatusBadRequest, "INVALID_COORDINATES", "Coordinates must be finite numbers")
		return 0, 0, false
	}
	if lat < -90 || lat > 90 {
		respondWithError(w, http.StatusBadRequest, "INVALID_COORDINATES", "Latitude must be between -90 and 90")
		return 0, 0, false
	}
	if lng < -180 || lng > 180 {
		respondWithError(w, http.StatusBadRequest, "INVALID_COORDINATES", "Longitude must be between -180 and 180")
		return 0, 0, false
	}
	return lat, lng, true
}
