package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type TripHandlers struct {
	tripService *service.TripService
	logger      *logrus.Logger
}

func NewTripHandlers(tripService *service.TripService, logger *logrus.Logger) *TripHandlers {
	return &TripHandlers{tripService: tripService, logger: logger}
}

// GET /api/v1/de/trip
// Returns the DE's current active trip with full task details.
func (h *TripHandlers) GetCurrentTrip(w http.ResponseWriter, r *http.Request) {
	phone, _ := r.Context().Value("phone").(string)

	trip, err := h.tripService.GetCurrentTrip(r.Context(), phone)
	if err != nil {
		h.logger.WithError(err).Error("failed to get current trip")
		h.respondWithError(w, http.StatusInternalServerError, "TRIP_FETCH_FAILED", "Failed to fetch trip")
		return
	}
	if trip == nil {
		h.respondWithJSON(w, http.StatusOK, map[string]interface{}{"trip": nil})
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]interface{}{"trip": trip})
}

// POST /api/v1/trip/{tripId}/task/{taskId}/status/update
// Body: { "status": "completed" } or { "status": "reached", "otp": "4821" }
func (h *TripHandlers) UpdateTaskStatus(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	tripID := vars["tripId"]
	taskID := vars["taskId"]
	phone, _ := r.Context().Value("phone").(string)

	if strings.TrimSpace(tripID) == "" || strings.TrimSpace(taskID) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "tripId and taskId are required")
		return
	}

	var req struct {
		Status string `json:"status"`
		OTP    string `json:"otp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if strings.TrimSpace(req.Status) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "status is required")
		return
	}

	newStatus := models.TaskStatus(req.Status)
	err := h.tripService.UpdateTaskStatus(r.Context(), tripID, taskID, phone, newStatus, req.OTP)
	if err != nil {
		errStr := err.Error()
		switch {
		case strings.Contains(errStr, "not found"):
			h.respondWithError(w, http.StatusNotFound, "NOT_FOUND", errStr)
		case strings.Contains(errStr, "forbidden"):
			h.respondWithError(w, http.StatusForbidden, "FORBIDDEN", "Trip is not assigned to you")
		case strings.Contains(errStr, "trip_closed"):
			h.respondWithError(w, http.StatusBadRequest, "TRIP_ALREADY_CLOSED", errStr)
		case strings.Contains(errStr, "prerequisite_incomplete"):
			h.respondWithError(w, http.StatusBadRequest, "PREREQUISITE_TASK_INCOMPLETE", errStr)
		case strings.Contains(errStr, "invalid_transition"):
			h.respondWithError(w, http.StatusBadRequest, "INVALID_TASK_TRANSITION", errStr)
		case strings.Contains(errStr, "invalid OTP"):
			h.respondWithError(w, http.StatusBadRequest, "INVALID_OTP", "Incorrect OTP")
		default:
			h.logger.WithError(err).Error("failed to update task status")
			h.respondWithError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update task status")
		}
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *TripHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func (h *TripHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{
		Error: ErrorDetail{Code: code, Message: message},
	})
}
