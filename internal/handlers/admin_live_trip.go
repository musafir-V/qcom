package handlers

import (
	"net/http"
	"strings"

	"github.com/gorilla/mux"
)

type liveTripResponse struct {
	OrderID string `json:"order_id"`
	TripID  string `json:"trip_id"`
	Status  string `json:"status"`
	StoreID string `json:"store_id"`
}

// GetLiveTrip handles GET /admin/orders/{orderId}/live-trip.
// Returns the trip POST /admin/assign would update, including when its status
// is no longer created. 404 when the order has no live trip.
func (h *AdminTripsByOrdersHandlers) GetLiveTrip(w http.ResponseWriter, r *http.Request) {
	orderID := strings.TrimSpace(mux.Vars(r)["orderId"])
	if orderID == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "order_id is required")
		return
	}
	if h.live == nil {
		h.respondWithError(w, http.StatusInternalServerError, "LIVE_TRIP_FETCH_FAILED", "Failed to fetch live trip")
		return
	}

	trip, err := h.live.LiveTripByOrderID(r.Context(), orderID)
	if err != nil {
		h.logger.WithError(err).WithField("order_id", orderID).Error("live trip: lookup failed")
		h.respondWithError(w, http.StatusInternalServerError, "LIVE_TRIP_FETCH_FAILED", "Failed to fetch live trip")
		return
	}
	if trip == nil {
		h.respondWithError(w, http.StatusNotFound, "NO_LIVE_TRIP", "No live trip for this order")
		return
	}

	h.respondWithJSON(w, http.StatusOK, liveTripResponse{
		OrderID: trip.OrderID,
		TripID:  trip.TripID,
		Status:  string(trip.Status),
		StoreID: trip.StoreID,
	})
}
