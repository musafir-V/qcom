package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

// AdminStoreHandlers powers admin darkstore-onboarding flows. Create-only in
// v1: no list/detail/edit endpoints. Sits behind RequireAdminAuth.
type AdminStoreHandlers struct {
	darkstoreRepo *repository.DarkstoreRepository
	logger        *logrus.Logger
}

func NewAdminStoreHandlers(darkstoreRepo *repository.DarkstoreRepository, logger *logrus.Logger) *AdminStoreHandlers {
	return &AdminStoreHandlers{darkstoreRepo: darkstoreRepo, logger: logger}
}

type createDarkstoreRequest struct {
	Name      string   `json:"name"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	// Polygon is a raw textarea of "lat,lng" lines from the client — see
	// parsePolygonLines. Optional; empty is valid.
	Polygon  string `json:"polygon"`
	OpensAt  string `json:"opens_at"`
	ClosesAt string `json:"closes_at"`
}

// POST /api/v1/admin/darkstores
func (h *AdminStoreHandlers) CreateDarkstore(w http.ResponseWriter, r *http.Request) {
	var req createDarkstoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "name is required")
		return
	}
	if req.Latitude == nil {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "latitude is required")
		return
	}
	if req.Longitude == nil {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "longitude is required")
		return
	}
	if *req.Latitude < -90 || *req.Latitude > 90 {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_COORDINATES", "Latitude must be between -90 and 90")
		return
	}
	if *req.Longitude < -180 || *req.Longitude > 180 {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_COORDINATES", "Longitude must be between -180 and 180")
		return
	}

	polygon, err := parsePolygonLines(req.Polygon)
	if err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_POLYGON", err.Error())
		return
	}

	req.OpensAt = strings.TrimSpace(req.OpensAt)
	req.ClosesAt = strings.TrimSpace(req.ClosesAt)
	if req.OpensAt == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "opens_at is required")
		return
	}
	if req.ClosesAt == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "closes_at is required")
		return
	}
	probe := models.Darkstore{OpensAt: req.OpensAt, ClosesAt: req.ClosesAt}
	if !probe.ValidOperatingHours() {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_OPERATING_HOURS", "opens_at/closes_at must be HH:MM and closes_at must be after opens_at")
		return
	}

	ds, err := h.darkstoreRepo.Create(r.Context(), repository.CreateDarkstoreInput{
		Name:      req.Name,
		Latitude:  *req.Latitude,
		Longitude: *req.Longitude,
		Polygon:   polygon,
		OpensAt:   req.OpensAt,
		ClosesAt:  req.ClosesAt,
	})
	if err != nil {
		h.logger.WithError(err).Error("admin: failed to create darkstore")
		h.respondWithError(w, http.StatusInternalServerError, "DARKSTORE_CREATE_FAILED", "Failed to create darkstore")
		return
	}

	h.respondWithJSON(w, http.StatusCreated, map[string]interface{}{
		"darkstore_id": ds.DarkstoreID,
		"name":         ds.Name,
		"is_active":    ds.IsActive,
	})
}

// parsePolygonLines parses one "lat,lng" pair per non-blank line. Empty input
// returns (nil, nil) — polygon is optional. A non-empty input with fewer than
// 3 valid points, or any malformed/out-of-range line, is an error.
func parsePolygonLines(raw string) ([]models.PolygonPoint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	var points []models.PolygonPoint
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) != 2 {
			return nil, fmt.Errorf("polygon line %q must be \"lat,lng\"", line)
		}
		lat, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		if err != nil {
			return nil, fmt.Errorf("polygon line %q has a non-numeric latitude", line)
		}
		lng, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil {
			return nil, fmt.Errorf("polygon line %q has a non-numeric longitude", line)
		}
		if lat < -90 || lat > 90 || lng < -180 || lng > 180 {
			return nil, fmt.Errorf("polygon line %q has out-of-range coordinates", line)
		}
		points = append(points, models.PolygonPoint{Lat: lat, Lng: lng})
	}
	if len(points) > 0 && len(points) < 3 {
		return nil, fmt.Errorf("polygon must have at least 3 points (got %d)", len(points))
	}
	return points, nil
}

func (h *AdminStoreHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func (h *AdminStoreHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{Error: ErrorDetail{Code: code, Message: message}})
}
