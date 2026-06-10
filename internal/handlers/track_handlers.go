package handlers

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/service"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

const etaMinutes = 15 // fixed 15-minute delivery promise

type TrackHandlers struct {
	tripRepo   *repository.TripRepository
	deRepo     *repository.DERepository
	javaClient *service.JavaOrderClient
	logger     *logrus.Logger
}

func NewTrackHandlers(
	tripRepo *repository.TripRepository,
	deRepo *repository.DERepository,
	javaClient *service.JavaOrderClient,
	logger *logrus.Logger,
) *TrackHandlers {
	return &TrackHandlers{
		tripRepo:   tripRepo,
		deRepo:     deRepo,
		javaClient: javaClient,
		logger:     logger,
	}
}

type TrackResponse struct {
	TripStatus string      `json:"trip_status"`
	DEName     *string     `json:"de_name"`
	OTP        *string     `json:"otp"`
	ETA        *ETAPayload `json:"eta"`
}

type ETAPayload struct {
	ExpiresAt        string  `json:"expires_at"`
	RemainingMinutes int     `json:"remaining_minutes"`
	IsDelayed        bool    `json:"is_delayed"`
	Message          *string `json:"message"`
}

// Track handles GET /api/v1/orders/{orderId}/track.
// Requires customer JWT auth.
func (h *TrackHandlers) Track(w http.ResponseWriter, r *http.Request) {
	orderID := mux.Vars(r)["orderId"]
	if strings.TrimSpace(orderID) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "orderId is required")
		return
	}

	// Look up trip by order ID
	trip, err := h.tripRepo.GetByOrderID(r.Context(), orderID)
	if err != nil {
		h.logger.WithError(err).Error("track: failed to query trip by order ID")
		h.respondWithError(w, http.StatusInternalServerError, "FETCH_FAILED", "Failed to fetch trip")
		return
	}

	// No trip yet — verify order exists in Java then return finding_driver
	if trip == nil {
		javaStatus, err := h.javaClient.GetOrderStatus(r.Context(), orderID)
		if err != nil || javaStatus == "NOT_FOUND" {
			h.respondWithError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "Order not found")
			return
		}
		h.respondWithJSON(w, http.StatusOK, TrackResponse{
			TripStatus: "finding_driver",
			DEName:     nil,
			OTP:        nil,
			ETA:        nil,
		})
		return
	}

	// Completed or cancelled trips return an error — customer should see summary screen
	if trip.Status == models.TripStatusCompleted {
		h.respondWithError(w, http.StatusBadRequest, "TRIP_COMPLETED", "This order has already been delivered.")
		return
	}
	if trip.Status == models.TripStatusCancelled {
		h.respondWithError(w, http.StatusBadRequest, "TRIP_CANCELLED", "This order has been cancelled.")
		return
	}

	// Build response
	response := TrackResponse{
		TripStatus: string(trip.Status),
	}

	// Driver is revealed to the customer only once they have accepted — never
	// during the assigned (pending-accept) window, which may end in a reject.
	committed := trip.Status == models.TripStatusAccepted || trip.Status == models.TripStatusOutForDelivery
	if committed && trip.DEPhone != "" {
		de, err := h.deRepo.GetByPhone(r.Context(), trip.DEPhone)
		if err == nil && de != nil {
			response.DEName = &de.Name
		}
	}

	// OTP — shown only once the driver has committed and the drop is still open.
	dropTask := trip.DropTask()
	if committed && dropTask != nil && dropTask.Status == models.TaskStatusCreated {
		otp := dropTask.OTP
		response.OTP = &otp
	}

	// ETA — only meaningful once trip is created (has a created_at)
	if trip.CreatedAt != "" {
		response.ETA = computeETA(trip.CreatedAt)
	}

	// Customer sees "finding_driver" until a driver accepts — the assigned
	// (pending-accept) window and any reject churn stay invisible.
	if trip.Status == models.TripStatusCreated || trip.Status == models.TripStatusAssigned {
		response.TripStatus = "finding_driver"
	}

	h.respondWithJSON(w, http.StatusOK, response)
}

// computeETA builds the ETA payload from trip.CreatedAt.
// All time math uses Africa/Lusaka timezone.
func computeETA(createdAtUTC string) *ETAPayload {
	createdAt, err := time.Parse(time.RFC3339, createdAtUTC)
	if err != nil {
		return nil
	}

	loc := timezone.ZambiaLocation()
	createdAtZambia := createdAt.In(loc)
	expiresAt := createdAtZambia.Add(etaMinutes * time.Minute)
	now := timezone.Now()

	elapsed := now.Sub(createdAtZambia).Minutes()
	remaining := etaMinutes - elapsed
	remainingInt := int(math.Max(0, math.Ceil(remaining)))
	isDelayed := remaining <= 0

	eta := &ETAPayload{
		ExpiresAt:        expiresAt.Format(time.RFC3339),
		RemainingMinutes: remainingInt,
		IsDelayed:        isDelayed,
	}

	if isDelayed {
		msg := "Your delivery is running delayed. Please contact the driver for support."
		eta.Message = &msg
	}

	return eta
}

func (h *TrackHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func (h *TrackHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{
		Error: ErrorDetail{Code: code, Message: message},
	})
}
