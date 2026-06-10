package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

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
