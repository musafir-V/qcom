package handlers

import (
	"encoding/json"
	"errors"
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
// Body: { "status": "completed" }
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
		status, code := classifyTaskUpdateError(err)
		if status == http.StatusInternalServerError {
			h.logger.WithError(err).Error("failed to update task status")
			h.respondWithError(w, status, code, "Failed to update task status")
			return
		}
		h.respondWithError(w, status, code, err.Error())
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// classifyTaskUpdateError maps a TripService.UpdateTaskStatus error to an HTTP
// status code and a machine-readable error code using errors.Is against the
// service package's sentinel errors. Unrecognized errors map to 500.
func classifyTaskUpdateError(err error) (status int, code string) {
	switch {
	case errors.Is(err, service.ErrTripNotFound), errors.Is(err, service.ErrTaskNotFound):
		return http.StatusNotFound, "NOT_FOUND"
	case errors.Is(err, service.ErrTripForbidden):
		return http.StatusForbidden, "FORBIDDEN"
	case errors.Is(err, service.ErrTripClosed):
		return http.StatusBadRequest, "TRIP_ALREADY_CLOSED"
	case errors.Is(err, service.ErrPrerequisiteIncomplete):
		return http.StatusBadRequest, "PREREQUISITE_TASK_INCOMPLETE"
	case errors.Is(err, service.ErrInvalidOTP):
		return http.StatusBadRequest, "INVALID_OTP"
	case errors.Is(err, service.ErrInvalidTransition):
		return http.StatusBadRequest, "INVALID_TASK_TRANSITION"
	default:
		return http.StatusInternalServerError, "UPDATE_FAILED"
	}
}

// POST /api/v1/trip/{tripId}/accept
func (h *TripHandlers) AcceptTrip(w http.ResponseWriter, r *http.Request) {
	h.acceptOrReject(w, r, true)
}

// POST /api/v1/trip/{tripId}/reject
func (h *TripHandlers) RejectTrip(w http.ResponseWriter, r *http.Request) {
	h.acceptOrReject(w, r, false)
}

func (h *TripHandlers) acceptOrReject(w http.ResponseWriter, r *http.Request, accept bool) {
	tripID := mux.Vars(r)["tripId"]
	phone, _ := r.Context().Value("phone").(string)

	if strings.TrimSpace(tripID) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "tripId is required")
		return
	}

	var err error
	if accept {
		err = h.tripService.AcceptTrip(r.Context(), tripID, phone)
	} else {
		err = h.tripService.RejectTrip(r.Context(), tripID, phone)
	}
	if err != nil {
		status, code := classifyAcceptRejectError(err)
		if status == http.StatusInternalServerError {
			h.logger.WithError(err).Error("failed to accept/reject trip")
			h.respondWithError(w, status, code, "Failed to update trip")
			return
		}
		h.respondWithError(w, status, code, err.Error())
		return
	}

	result := "accepted"
	if !accept {
		result = "rejected"
	}
	h.respondWithJSON(w, http.StatusOK, map[string]string{"status": result})
}

// classifyAcceptRejectError maps TripService.AcceptTrip/RejectTrip errors to
// HTTP status codes using errors.Is against the service sentinels.
func classifyAcceptRejectError(err error) (status int, code string) {
	switch {
	case errors.Is(err, service.ErrTripNotFound):
		return http.StatusNotFound, "NOT_FOUND"
	case errors.Is(err, service.ErrTripForbidden):
		return http.StatusForbidden, "FORBIDDEN"
	case errors.Is(err, service.ErrInvalidTripTransition):
		return http.StatusConflict, "INVALID_TRIP_STATE"
	default:
		return http.StatusInternalServerError, "ACTION_FAILED"
	}
}

// POST /api/v1/trip/{tripId}/verify-pickup
// Body: { "order_id": "..." }  — order_id decoded from the scanned bill QR.
func (h *TripHandlers) VerifyPickup(w http.ResponseWriter, r *http.Request) {
	tripID := mux.Vars(r)["tripId"]
	phone, _ := r.Context().Value("phone").(string)

	if strings.TrimSpace(tripID) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "tripId is required")
		return
	}

	var req struct {
		OrderID string `json:"order_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}

	if err := h.tripService.VerifyPickup(r.Context(), tripID, phone, req.OrderID); err != nil {
		status, code := classifyVerifyPickupError(err)
		if status == http.StatusInternalServerError {
			h.logger.WithError(err).Error("failed to verify pickup")
			h.respondWithError(w, status, code, "Failed to verify pickup")
			return
		}
		h.respondWithError(w, status, code, err.Error())
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]string{"status": "verified"})
}

// classifyVerifyPickupError maps VerifyPickup errors to HTTP codes.
func classifyVerifyPickupError(err error) (status int, code string) {
	switch {
	case errors.Is(err, service.ErrTripNotFound):
		return http.StatusNotFound, "NOT_FOUND"
	case errors.Is(err, service.ErrTripForbidden):
		return http.StatusForbidden, "FORBIDDEN"
	case errors.Is(err, service.ErrPickupOrderMismatch):
		return http.StatusBadRequest, "PICKUP_ORDER_MISMATCH"
	case errors.Is(err, service.ErrInvalidTripTransition):
		return http.StatusConflict, "INVALID_TRIP_STATE"
	default:
		return http.StatusInternalServerError, "VERIFY_FAILED"
	}
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
