package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"

	"github.com/qcom/qcom/internal/middleware"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

// addressLineGeocoder is the slice of GoogleGeocoder this handler needs,
// declared as an interface so tests can inject a stub.
type addressLineGeocoder interface {
	ReverseGeocodeAddressLine(ctx context.Context, lat, lng float64) (service.AddressLineResult, error)
}

type GeocodeHandlers struct {
	geocoder addressLineGeocoder
	logger   *logrus.Logger
}

func NewGeocodeHandlers(geocoder addressLineGeocoder, logger *logrus.Logger) *GeocodeHandlers {
	return &GeocodeHandlers{geocoder: geocoder, logger: logger}
}

type geocodeReverseRequest struct {
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}

// ReverseGeocode resolves a coordinate to a route-aware address line for the
// address form. It fails open: no result → 200 with empty address_line; a
// geocoder error → 502 (the app proceeds with an empty building_and_floor).
func (h *GeocodeHandlers) ReverseGeocode(w http.ResponseWriter, r *http.Request) {
	entityType, _ := r.Context().Value("entity_type").(string)
	userID, _ := r.Context().Value("entity_id").(string)
	if entityType != middleware.EntityTypeGuest && userID == "" {
		respondWithError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Entity ID not found in token")
		return
	}

	var req geocodeReverseRequest
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
	if math.IsNaN(*req.Latitude) || math.IsInf(*req.Latitude, 0) ||
		math.IsNaN(*req.Longitude) || math.IsInf(*req.Longitude, 0) {
		respondWithError(w, http.StatusBadRequest, "INVALID_COORDINATES", "Coordinates must be finite numbers")
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

	result, err := h.geocoder.ReverseGeocodeAddressLine(r.Context(), *req.Latitude, *req.Longitude)
	if err != nil {
		if errors.Is(err, service.ErrNoGeocodeResult) {
			respondWithJSON(w, http.StatusOK, map[string]interface{}{"data": service.AddressLineResult{}})
			return
		}
		h.logger.WithError(err).Warn("Reverse geocode failed")
		respondWithError(w, http.StatusBadGateway, "GEOCODE_FAILED", "Could not resolve location")
		return
	}

	respondWithJSON(w, http.StatusOK, map[string]interface{}{"data": result})
}
