package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type AdminHandlers struct {
	adminService *service.AdminService
	logger       *logrus.Logger
}

func NewAdminHandlers(adminService *service.AdminService, logger *logrus.Logger) *AdminHandlers {
	return &AdminHandlers{adminService: adminService, logger: logger}
}

// POST /api/v1/admin/assign
// Body: { "order_id": "...", "driver_phone": "..." }
func (h *AdminHandlers) AssignOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OrderID     string `json:"order_id"`
		DriverPhone string `json:"driver_phone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if strings.TrimSpace(req.OrderID) == "" || strings.TrimSpace(req.DriverPhone) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "order_id and driver_phone are required")
		return
	}

	if err := h.adminService.AssignOrder(r.Context(), req.OrderID, req.DriverPhone); err != nil {
		status, code := classifyAdminAssignError(err)
		if status == http.StatusInternalServerError {
			h.logger.WithError(err).Error("admin assign failed")
			h.respondWithError(w, status, code, "Failed to assign order")
			return
		}
		h.respondWithError(w, status, code, err.Error())
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]string{"status": "assigned"})
}

func classifyAdminAssignError(err error) (status int, code string) {
	switch {
	case errors.Is(err, service.ErrTripNotFound):
		return http.StatusNotFound, "ORDER_NOT_FOUND"
	case errors.Is(err, service.ErrInvalidTripTransition):
		return http.StatusConflict, "ORDER_NOT_ASSIGNABLE"
	case errors.Is(err, service.ErrDENotFound):
		return http.StatusNotFound, "DRIVER_NOT_FOUND"
	case errors.Is(err, service.ErrDENotEligible):
		return http.StatusConflict, "DRIVER_NOT_ELIGIBLE"
	default:
		return http.StatusInternalServerError, "ASSIGN_FAILED"
	}
}

// GET /api/v1/admin/trips/{trip_id}/reassign-candidates
// Riders on duty at the trip's store who could take it over.
func (h *AdminHandlers) ReassignCandidates(w http.ResponseWriter, r *http.Request) {
	tripID := strings.TrimSpace(mux.Vars(r)["trip_id"])
	if tripID == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "trip_id is required")
		return
	}

	candidates, err := h.adminService.ReassignCandidates(r.Context(), tripID)
	if err != nil {
		status, code := classifyReassignError(err)
		if status == http.StatusInternalServerError {
			h.logger.WithError(err).Error("admin: failed to list reassign candidates")
			h.respondWithError(w, status, code, "Failed to list candidates")
			return
		}
		h.respondWithError(w, status, code, err.Error())
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]interface{}{"candidates": candidates})
}

// POST /api/v1/admin/trips/{trip_id}/reassign
// Body: { "to_de_phone": "+2609...", "reason_code": "bike_breakdown", "note": "" }
func (h *AdminHandlers) ReassignTrip(w http.ResponseWriter, r *http.Request) {
	tripID := strings.TrimSpace(mux.Vars(r)["trip_id"])
	if tripID == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "trip_id is required")
		return
	}

	var req struct {
		ToDEPhone  string `json:"to_de_phone"`
		ReasonCode string `json:"reason_code"`
		Note       string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	phone := strings.TrimSpace(req.ToDEPhone)
	if phone != "" && !strings.HasPrefix(phone, "+") {
		phone = "+" + phone
	}
	if phone == "" || strings.TrimSpace(req.ReasonCode) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "to_de_phone and reason_code are required")
		return
	}

	// Set by RequireAdminAuth; same pattern as the dispute handlers.
	adminUsername, _ := r.Context().Value("entity_id").(string)

	// note is persisted onto the trip item, which riders poll on every
	// GetCurrentTrip — bound it so an admin can't balloon that payload.
	// Truncate by rune, not byte, so a multi-byte character isn't split.
	note := truncateRunes(strings.TrimSpace(req.Note), reassignNoteMaxRunes)

	if err := h.adminService.ReassignTrip(
		r.Context(), tripID, phone, strings.TrimSpace(req.ReasonCode), note, adminUsername,
	); err != nil {
		status, code := classifyReassignError(err)
		if status == http.StatusInternalServerError {
			h.logger.WithError(err).Error("admin: trip reassignment failed")
			h.respondWithError(w, status, code, "Failed to reassign trip")
			return
		}
		h.respondWithError(w, status, code, err.Error())
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]string{"status": "reassigned"})
}

func classifyReassignError(err error) (status int, code string) {
	switch {
	case errors.Is(err, service.ErrTripNotFound):
		return http.StatusNotFound, "TRIP_NOT_FOUND"
	case errors.Is(err, service.ErrDENotFound):
		return http.StatusNotFound, "DRIVER_NOT_FOUND"
	case errors.Is(err, service.ErrTripNotReassignable):
		return http.StatusConflict, "TRIP_NOT_REASSIGNABLE"
	case errors.Is(err, service.ErrDENotEligible):
		return http.StatusConflict, "DRIVER_NOT_ELIGIBLE"
	case errors.Is(err, service.ErrDriverWrongStore):
		return http.StatusConflict, "DRIVER_WRONG_STORE"
	case errors.Is(err, service.ErrSameDriver):
		return http.StatusBadRequest, "SAME_DRIVER"
	case errors.Is(err, service.ErrInvalidReasonCode):
		return http.StatusBadRequest, "INVALID_REASON_CODE"
	case errors.Is(err, service.ErrReassignConflict):
		return http.StatusConflict, "REASSIGN_CONFLICT"
	default:
		return http.StatusInternalServerError, "REASSIGN_FAILED"
	}
}

// reassignNoteMaxRunes bounds the reassignment note. The trip item carrying
// it is polled by riders on every GetCurrentTrip, so an unbounded admin-typed
// note must not be allowed to bloat that payload.
const reassignNoteMaxRunes = 500

// truncateRunes truncates s to at most n runes, splitting on rune boundaries
// so a multi-byte character is never cut in half.
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func (h *AdminHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func (h *AdminHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{
		Error: ErrorDetail{Code: code, Message: message},
	})
}
