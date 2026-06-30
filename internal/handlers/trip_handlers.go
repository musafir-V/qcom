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
	tripService   *service.TripService
	uploadService *service.UploadService
	logger        *logrus.Logger
}

func NewTripHandlers(tripService *service.TripService, uploadService *service.UploadService, logger *logrus.Logger) *TripHandlers {
	return &TripHandlers{tripService: tripService, uploadService: uploadService, logger: logger}
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
		Status     string `json:"status"`
		OTP        string `json:"otp"`
		PhotoS3Key string `json:"photo_s3_key"`
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
	err := h.tripService.UpdateTaskStatus(r.Context(), tripID, taskID, phone, newStatus, req.OTP, req.PhotoS3Key)
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

// POST /api/v1/trip/{tripId}/task/{taskId}/photo/presign
// Body: { "file_name": "photo.jpg", "file_type": "image/jpeg", "file_size": 1048576 }
func (h *TripHandlers) PresignTaskPhoto(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	tripID := vars["tripId"]
	taskID := vars["taskId"]
	phone, _ := r.Context().Value("phone").(string)
	deID, _ := r.Context().Value("entity_id").(string)

	var req struct {
		FileName string `json:"file_name"`
		FileType string `json:"file_type"`
		FileSize int64  `json:"file_size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if strings.TrimSpace(req.FileName) == "" || strings.TrimSpace(req.FileType) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "file_name and file_type are required")
		return
	}

	trip, task, err := h.tripService.GetTripForPhotoPresign(r.Context(), tripID, taskID, phone)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrTripNotFound), errors.Is(err, service.ErrTaskNotFound):
			h.respondWithError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		case errors.Is(err, service.ErrTripForbidden):
			h.respondWithError(w, http.StatusForbidden, "FORBIDDEN", "Trip not assigned to you")
		default:
			h.logger.WithError(err).Error("GetTripForPhotoPresign failed")
			h.respondWithError(w, http.StatusInternalServerError, "PRESIGN_FAILED", "Failed to generate upload URL")
		}
		return
	}

	result, err := h.uploadService.PresignTripTaskPhoto(r.Context(), trip.OrderID, string(task.Type), deID, req.FileType, req.FileSize)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrUnsupportedPhotoMimeType):
			h.respondWithError(w, http.StatusBadRequest, "MIME_NOT_ALLOWED", err.Error())
		case errors.Is(err, service.ErrPhotoFileTooLarge):
			h.respondWithError(w, http.StatusBadRequest, "FILE_TOO_LARGE", err.Error())
		default:
			h.logger.WithError(err).Error("PresignTripTaskPhoto failed")
			h.respondWithError(w, http.StatusInternalServerError, "PRESIGN_FAILED", "Failed to generate upload URL")
		}
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]interface{}{
		"upload_url":         result.UploadURL,
		"object_key":         result.ObjectKey,
		"expires_in_seconds": result.ExpiresInSeconds,
	})
}

// POST /internal/v1/trips/cancel-by-order
// Body: { "order_id": "...", "reason": "..." }
// Called by Java order-service when an order is cancelled. Fail-open on the Java side —
// this handler returns 200 for all non-validation outcomes so Java treats errors as skip.
func (h *TripHandlers) CancelTripByOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OrderID string `json:"order_id"`
		Reason  string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if strings.TrimSpace(req.OrderID) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "order_id is required")
		return
	}

	if err := h.tripService.CancelTripByOrder(r.Context(), req.OrderID, req.Reason); err != nil {
		h.logger.WithError(err).WithField("order_id", req.OrderID).Error("CancelTripByOrder failed")
		// Return 200 so Java fail-open treats this as a skipped side-effect, not a hard error.
		h.respondWithJSON(w, http.StatusOK, map[string]string{"status": "error", "reason": err.Error()})
		return
	}

	h.respondWithJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /internal/v1/trips/payment/update
// Body: { "order_id": "...", "payment_method": "AIRTEL_MONEY", "grand_total": 250.0, "currency": "ZMW" }
// Called by Java order-service when an order's payment method changes (e.g. a COD
// order is paid online). Re-snapshots the trip's payment and pushes the rider.
// Responses:
//   200 {"updated": true}                                 — trip payment updated
//   200 {"updated": false, "reason": "no_active_trip"}    — no trip exists yet (no-op)
//   409 {"updated": false, "reason": "trip_terminal"}     — trip already closed; not updated
func (h *TripHandlers) UpdateTripPaymentByOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OrderID       string  `json:"order_id"`
		PaymentMethod string  `json:"payment_method"`
		GrandTotal    float64 `json:"grand_total"`
		Currency      string  `json:"currency"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondWithError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if strings.TrimSpace(req.OrderID) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "order_id is required")
		return
	}
	if strings.TrimSpace(req.PaymentMethod) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_FIELD", "payment_method is required")
		return
	}

	result, err := h.tripService.UpdateTripPayment(r.Context(), service.PaymentUpdateInput{
		OrderID:       req.OrderID,
		PaymentMethod: req.PaymentMethod,
		GrandTotal:    req.GrandTotal,
		Currency:      req.Currency,
	})
	if err != nil {
		h.logger.WithError(err).WithField("order_id", req.OrderID).Error("UpdateTripPayment failed")
		h.respondWithError(w, http.StatusInternalServerError, "PAYMENT_UPDATE_FAILED", "Failed to update trip payment")
		return
	}

	status := http.StatusOK
	if result.Reason == "trip_terminal" {
		status = http.StatusConflict
	}
	h.respondWithJSON(w, status, map[string]interface{}{
		"updated": result.Updated,
		"reason":  result.Reason,
	})
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
