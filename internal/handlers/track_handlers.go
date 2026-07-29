package handlers

import (
	"context"
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

// Dependency seams — the concrete service/repositories satisfy these.
type trackOrderFetcher interface {
	GetOrderRaw(ctx context.Context, orderID string) (map[string]json.RawMessage, error)
}
type trackTripGetter interface {
	GetByOrderID(ctx context.Context, orderID string) (*models.Trip, error)
}
type trackDEResolver interface {
	GetByPhone(ctx context.Context, phone string) (*models.DeliveryExecutive, error)
}

type TrackHandlers struct {
	tripRepo   trackTripGetter
	deRepo     trackDEResolver
	javaClient trackOrderFetcher
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

type ETAPayload struct {
	ExpiresAt        string  `json:"expires_at"`
	RemainingMinutes int     `json:"remaining_minutes"`
	IsDelayed        bool    `json:"is_delayed"`
	Message          *string `json:"message"`
}

// Track handles GET /api/v1/orders/{orderId}/track.
// Returns the full order-details payload (from order-service) enriched with the
// trip-derived fields otp, de_name, and eta. Requires customer JWT auth.
func (h *TrackHandlers) Track(w http.ResponseWriter, r *http.Request) {
	orderID := mux.Vars(r)["orderId"]
	if strings.TrimSpace(orderID) == "" {
		respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "orderId is required")
		return
	}

	// Order payload is required — hard-fail if we can't load it.
	order, err := h.javaClient.GetOrderRaw(r.Context(), orderID)
	if err != nil {
		h.logger.WithError(err).Error("track: failed to fetch order from order-service")
		respondWithError(w, http.StatusBadGateway, "FETCH_FAILED", "Failed to fetch order")
		return
	}
	if order == nil {
		respondWithError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "Order not found")
		return
	}

	// Trip enrichment is best-effort — a lookup failure degrades to null fields.
	trip, err := h.tripRepo.GetByOrderID(r.Context(), orderID)
	if err != nil {
		h.logger.WithError(err).Error("track: trip lookup failed, returning order without tracking fields")
		trip = nil
	}

	// ETA is anchored on order creation time and is independent of the trip.
	// It is suppressed once the order is terminal (DELIVERED/CANCELLED), or as a
	// safety net when an existing trip is already terminal (handles the brief
	// window before Java syncs the order status).
	var eta *ETAPayload
	tripTerminal := trip != nil && (trip.Status == models.TripStatusCompleted || trip.Status == models.TripStatusCancelled)
	if !orderTerminal(order) && !tripTerminal {
		createdAt := orderCreatedAt(order)
		if createdAt == "" {
			h.logger.Warn("track: order createdAt missing; eta omitted")
		} else if eta = computeETA(createdAt); eta == nil {
			h.logger.WithField("created_at", createdAt).Warn("track: order createdAt unparseable; eta omitted")
		}
	}

	// Driver and OTP are revealed only once the driver has committed
	// (accepted/out_for_delivery) — never during the pending-accept window.
	var otp, deName *string
	if trip != nil {
		committed := trip.Status == models.TripStatusAccepted || trip.Status == models.TripStatusOutForDelivery
		if committed && trip.DEPhone != "" {
			de, derr := h.deRepo.GetByPhone(r.Context(), trip.DEPhone)
			if derr != nil {
				h.logger.WithError(derr).Warn("track: DE lookup failed; de_name omitted")
			} else if de != nil {
				deName = &de.Name
			}
		}
		if committed {
			if drop := trip.DropTask(); drop != nil && drop.Status == models.TaskStatusCreated {
				otp = &drop.OTP
			}
		}
	}

	if err := enrichOrderWithTracking(order, otp, deName, eta); err != nil {
		h.logger.WithError(err).Error("track: failed to enrich order payload")
		respondWithError(w, http.StatusInternalServerError, "ENRICH_FAILED", "Failed to build response")
		return
	}

	respondWithJSON(w, http.StatusOK, order)
}

// orderCreatedAt extracts the order's creation timestamp (the "createdAt" field)
// from the raw order payload. Returns "" when the field is absent or not a string.
func orderCreatedAt(order map[string]json.RawMessage) string {
	raw, ok := order["createdAt"]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// orderTerminal reports whether the order's "status" field is a terminal state
// (DELIVERED or CANCELLED) for which no ETA should be shown.
func orderTerminal(order map[string]json.RawMessage) bool {
	raw, ok := order["status"]
	if !ok {
		return false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DELIVERED", "CANCELLED":
		return true
	default:
		return false
	}
}

// computeETA builds the ETA payload from the order's createdAt timestamp.
// All time math uses Africa/Lusaka timezone. Returns nil when createdAt is
// unparseable.
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

// enrichOrderWithTracking injects the trip-derived tracking fields into the
// order object in place. nil pointers are written as JSON null.
func enrichOrderWithTracking(order map[string]json.RawMessage, otp *string, deName *string, eta *ETAPayload) error {
	for key, val := range map[string]any{"otp": otp, "de_name": deName, "eta": eta} {
		raw, err := json.Marshal(val)
		if err != nil {
			return err
		}
		order[key] = raw
	}
	return nil
}
