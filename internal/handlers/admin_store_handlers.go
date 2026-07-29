package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

// darkstoreStore is the subset of *repository.DarkstoreRepository the admin
// handlers depend on. Declared as an interface so the HTTP layer (filtering,
// sorting, response shaping) is unit-testable with a fake, without a live
// DynamoDB table. *repository.DarkstoreRepository satisfies it.
type darkstoreStore interface {
	GetByID(ctx context.Context, darkstoreID string) (*models.Darkstore, error)
	Create(ctx context.Context, in repository.CreateDarkstoreInput) (*models.Darkstore, error)
	Update(ctx context.Context, darkstoreID string, in repository.UpdateDarkstoreInput) (*models.Darkstore, error)
	SetActive(ctx context.Context, darkstoreID string, active bool) (*models.Darkstore, error)
	ListAll(ctx context.Context) ([]models.Darkstore, error)
	ListActive(ctx context.Context) ([]models.Darkstore, error)
}

// AdminStoreHandlers powers admin darkstore-onboarding flows. Supports
// create, list, get, partial update, and activate/deactivate. Sits behind
// RequireAdminAuth.
type AdminStoreHandlers struct {
	darkstoreRepo darkstoreStore
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

type updateDarkstoreRequest struct {
	Name      *string  `json:"name"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	// Polygon: nil = leave unchanged; "" = explicitly clear it back to empty;
	// non-empty = reparse via parsePolygonLines (same ≥3-point rule as create).
	Polygon              *string  `json:"polygon"`
	OpensAt              *string  `json:"opens_at"`
	ClosesAt             *string  `json:"closes_at"`
	PresenceRadiusMeters *float64 `json:"presence_radius_meters"`
}

// darkstoreDTO is the shared JSON response shape for all darkstore
// read/write endpoints. Always includes activation_ready/activation_blockers
// so the frontend never has to reimplement the activation gate.
func darkstoreDTO(ds *models.Darkstore) map[string]interface{} {
	return map[string]interface{}{
		"darkstore_id":           ds.DarkstoreID,
		"name":                   ds.Name,
		"latitude":               ds.Latitude,
		"longitude":              ds.Longitude,
		"polygon":                ds.Polygon,
		"presence_radius_meters": ds.EffectivePresenceRadiusMeters(),
		"is_active":              ds.IsActive,
		"opens_at":               ds.OpensAt,
		"closes_at":              ds.ClosesAt,
		"created_at":             ds.CreatedAt,
		"updated_at":             ds.UpdatedAt,
		"activation_ready":       ds.ReadyForActivation(),
		"activation_blockers":    ds.ActivationBlockers(),
	}
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

// GET /api/v1/admin/darkstores
// Returns active darkstores by default. Pass ?all=true (case-insensitive) to
// include inactive ones too — any other value falls back to the active-only
// default (lenient, never errors on a bad param). Results are sorted by
// darkstore_id ascending so the admin list order is stable across refreshes.
func (h *AdminStoreHandlers) ListDarkstores(w http.ResponseWriter, r *http.Request) {
	includeAll := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("all")), "true")

	var (
		darkstores []models.Darkstore
		err        error
	)
	if includeAll {
		darkstores, err = h.darkstoreRepo.ListAll(r.Context())
	} else {
		darkstores, err = h.darkstoreRepo.ListActive(r.Context())
	}
	if err != nil {
		h.logger.WithError(err).Error("admin: failed to list darkstores")
		h.respondWithError(w, http.StatusInternalServerError, "DARKSTORES_LIST_FAILED", "Failed to list darkstores")
		return
	}

	sort.Slice(darkstores, func(i, j int) bool {
		return darkstores[i].DarkstoreID < darkstores[j].DarkstoreID
	})

	// Always emit a non-nil slice so the JSON is {"darkstores": []} rather
	// than {"darkstores": null} when there are no stores.
	items := make([]map[string]interface{}, 0, len(darkstores))
	for i := range darkstores {
		items = append(items, darkstoreDTO(&darkstores[i]))
	}

	h.respondWithJSON(w, http.StatusOK, map[string]interface{}{"darkstores": items})
}

// GET /api/v1/admin/darkstores/{id}
func (h *AdminStoreHandlers) GetDarkstore(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	ds, err := h.darkstoreRepo.GetByID(r.Context(), id)
	if err != nil {
		h.logger.WithError(err).Error("admin: failed to get darkstore")
		h.respondWithError(w, http.StatusInternalServerError, "DARKSTORE_FETCH_FAILED", "Failed to fetch darkstore")
		return
	}
	if ds == nil {
		h.respondWithError(w, http.StatusNotFound, "DARKSTORE_NOT_FOUND", "Darkstore not found")
		return
	}
	h.respondWithJSON(w, http.StatusOK, darkstoreDTO(ds))
}

// PATCH /api/v1/admin/darkstores/{id}
// Partial update: only fields present in the request body are changed.
// Editing latitude/longitude/polygon is rejected while the store is active
// (DARKSTORE_LOCATION_LOCKED) — deactivate first.
func (h *AdminStoreHandlers) UpdateDarkstore(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	current, err := h.darkstoreRepo.GetByID(r.Context(), id)
	if err != nil {
		h.logger.WithError(err).Error("admin: failed to load darkstore for update")
		h.respondWithError(w, http.StatusInternalServerError, "DARKSTORE_FETCH_FAILED", "Failed to fetch darkstore")
		return
	}
	if current == nil {
		h.respondWithError(w, http.StatusNotFound, "DARKSTORE_NOT_FOUND", "Darkstore not found")
		return
	}

	var req updateDarkstoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if req.Name == nil && req.Latitude == nil && req.Longitude == nil && req.Polygon == nil &&
		req.OpensAt == nil && req.ClosesAt == nil && req.PresenceRadiusMeters == nil {
		h.respondWithError(w, http.StatusBadRequest, "EMPTY_UPDATE", "At least one field must be provided")
		return
	}

	if (req.Latitude != nil || req.Longitude != nil || req.Polygon != nil) && current.IsActive {
		h.respondWithError(w, http.StatusConflict, "DARKSTORE_LOCATION_LOCKED",
			"Deactivate the darkstore before editing latitude, longitude, or polygon")
		return
	}

	in := repository.UpdateDarkstoreInput{}

	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "name cannot be empty")
			return
		}
		in.Name = &name
	}
	if req.Latitude != nil {
		if *req.Latitude < -90 || *req.Latitude > 90 {
			h.respondWithError(w, http.StatusBadRequest, "INVALID_COORDINATES", "Latitude must be between -90 and 90")
			return
		}
		in.Latitude = req.Latitude
	}
	if req.Longitude != nil {
		if *req.Longitude < -180 || *req.Longitude > 180 {
			h.respondWithError(w, http.StatusBadRequest, "INVALID_COORDINATES", "Longitude must be between -180 and 180")
			return
		}
		in.Longitude = req.Longitude
	}
	if req.Polygon != nil {
		polygon, perr := parsePolygonLines(*req.Polygon)
		if perr != nil {
			h.respondWithError(w, http.StatusBadRequest, "INVALID_POLYGON", perr.Error())
			return
		}
		in.Polygon = &polygon
	}
	if req.OpensAt != nil || req.ClosesAt != nil {
		opens, closes := current.OpensAt, current.ClosesAt
		if req.OpensAt != nil {
			opens = strings.TrimSpace(*req.OpensAt)
		}
		if req.ClosesAt != nil {
			closes = strings.TrimSpace(*req.ClosesAt)
		}
		probe := models.Darkstore{OpensAt: opens, ClosesAt: closes}
		if !probe.ValidOperatingHours() {
			h.respondWithError(w, http.StatusBadRequest, "INVALID_OPERATING_HOURS",
				"opens_at/closes_at must be HH:MM and closes_at must be after opens_at")
			return
		}
		if req.OpensAt != nil {
			in.OpensAt = &opens
		}
		if req.ClosesAt != nil {
			in.ClosesAt = &closes
		}
	}
	if req.PresenceRadiusMeters != nil {
		if *req.PresenceRadiusMeters < 0 {
			h.respondWithError(w, http.StatusBadRequest, "INVALID_PRESENCE_RADIUS", "presence_radius_meters cannot be negative")
			return
		}
		in.PresenceRadiusMeters = req.PresenceRadiusMeters
	}

	updated, err := h.darkstoreRepo.Update(r.Context(), id, in)
	if err != nil {
		h.logger.WithError(err).Error("admin: failed to update darkstore")
		h.respondWithError(w, http.StatusInternalServerError, "DARKSTORE_UPDATE_FAILED", "Failed to update darkstore")
		return
	}
	if updated == nil {
		h.respondWithError(w, http.StatusNotFound, "DARKSTORE_NOT_FOUND", "Darkstore not found")
		return
	}
	h.respondWithJSON(w, http.StatusOK, darkstoreDTO(updated))
}

// POST /api/v1/admin/darkstores/{id}/activate
// Rejects with DARKSTORE_INCOMPLETE (409) if the store isn't ready per
// models.Darkstore.ActivationBlockers().
func (h *AdminStoreHandlers) ActivateDarkstore(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	ds, err := h.darkstoreRepo.GetByID(r.Context(), id)
	if err != nil {
		h.logger.WithError(err).Error("admin: failed to load darkstore for activation")
		h.respondWithError(w, http.StatusInternalServerError, "DARKSTORE_FETCH_FAILED", "Failed to fetch darkstore")
		return
	}
	if ds == nil {
		h.respondWithError(w, http.StatusNotFound, "DARKSTORE_NOT_FOUND", "Darkstore not found")
		return
	}
	if blockers := ds.ActivationBlockers(); len(blockers) > 0 {
		h.respondWithError(w, http.StatusConflict, "DARKSTORE_INCOMPLETE",
			"Darkstore is missing required fields: "+strings.Join(blockers, "; "))
		return
	}

	updated, err := h.darkstoreRepo.SetActive(r.Context(), id, true)
	if err != nil {
		h.logger.WithError(err).Error("admin: failed to activate darkstore")
		h.respondWithError(w, http.StatusInternalServerError, "DARKSTORE_ACTIVATE_FAILED", "Failed to activate darkstore")
		return
	}
	if updated == nil {
		h.respondWithError(w, http.StatusNotFound, "DARKSTORE_NOT_FOUND", "Darkstore not found")
		return
	}
	h.respondWithJSON(w, http.StatusOK, darkstoreDTO(updated))
}

// POST /api/v1/admin/darkstores/{id}/deactivate
// Unconditional — any active store can always be turned off, no gate.
func (h *AdminStoreHandlers) DeactivateDarkstore(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	updated, err := h.darkstoreRepo.SetActive(r.Context(), id, false)
	if err != nil {
		h.logger.WithError(err).Error("admin: failed to deactivate darkstore")
		h.respondWithError(w, http.StatusInternalServerError, "DARKSTORE_DEACTIVATE_FAILED", "Failed to deactivate darkstore")
		return
	}
	if updated == nil {
		h.respondWithError(w, http.StatusNotFound, "DARKSTORE_NOT_FOUND", "Darkstore not found")
		return
	}
	h.respondWithJSON(w, http.StatusOK, darkstoreDTO(updated))
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
	writeJSON(w, h.logger, status, payload)
}

func (h *AdminStoreHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{Error: ErrorDetail{Code: code, Message: message}})
}
