package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

const maxTripsByOrderIDs = 100

// tripByOrderLister is the slice of TripRepository this handler needs.
type tripByOrderLister interface {
	ListByOrderID(ctx context.Context, orderID string) ([]*models.Trip, error)
}

// AdminTripsByOrdersHandlers serves GET /admin/trips/by-orders.
// Mount behind RequireAdminAuth (parent router applies auth).
type AdminTripsByOrdersHandlers struct {
	lister tripByOrderLister
	logger *logrus.Logger
}

func NewAdminTripsByOrdersHandlers(repo *repository.TripRepository, logger *logrus.Logger) *AdminTripsByOrdersHandlers {
	return &AdminTripsByOrdersHandlers{lister: repo, logger: logger}
}

func newAdminTripsByOrdersHandlers(lister tripByOrderLister, logger *logrus.Logger) *AdminTripsByOrdersHandlers {
	return &AdminTripsByOrdersHandlers{lister: lister, logger: logger}
}

type tripsByOrderItem struct {
	OrderID    string  `json:"order_id"`
	DistanceKM float64 `json:"distance_km"`
	ReachedAt  string  `json:"reached_at,omitempty"`
	TripStatus string  `json:"trip_status"`
}

type tripsByOrdersResponse struct {
	Trips []tripsByOrderItem `json:"trips"`
}

// GetTripsByOrders handles GET /trips/by-orders?ids=ORD1,ORD2,...
func (h *AdminTripsByOrdersHandlers) GetTripsByOrders(w http.ResponseWriter, r *http.Request) {
	ids := parseOrderIDs(r.URL.Query().Get("ids"))
	if len(ids) == 0 {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "ids is required")
		return
	}
	if len(ids) > maxTripsByOrderIDs {
		h.respondWithError(w, http.StatusBadRequest, "TOO_MANY_IDS", "ids cannot exceed 100")
		return
	}

	out := make([]tripsByOrderItem, 0, len(ids))
	for _, id := range ids {
		trips, err := h.lister.ListByOrderID(r.Context(), id)
		if err != nil {
			h.logger.WithError(err).WithField("order_id", id).Error("trips by orders: list failed")
			h.respondWithError(w, http.StatusInternalServerError, "TRIPS_FETCH_FAILED", "Failed to fetch trips")
			return
		}
		chosen := service.ChooseTripForLegs(trips)
		if chosen == nil {
			continue
		}
		item := tripsByOrderItem{
			OrderID:    chosen.OrderID,
			DistanceKM: chosen.DistanceKM,
			TripStatus: string(chosen.Status),
		}
		if drop := chosen.DropTask(); drop != nil {
			item.ReachedAt = drop.ReachedAt
		}
		out = append(out, item)
	}
	h.respondWithJSON(w, http.StatusOK, tripsByOrdersResponse{Trips: out})
}

func parseOrderIDs(raw string) []string {
	parts := strings.Split(raw, ",")
	seen := make(map[string]struct{}, len(parts))
	ids := make([]string, 0, len(parts))
	for _, p := range parts {
		id := strings.TrimSpace(p)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func (h *AdminTripsByOrdersHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (h *AdminTripsByOrdersHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{Error: ErrorDetail{Code: code, Message: message}})
}
