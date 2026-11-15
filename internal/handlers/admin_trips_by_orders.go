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

// liveTripGetter is the live-trip lookup POST /admin/assign uses.
type liveTripGetter interface {
	LiveTripByOrderID(ctx context.Context, orderID string) (*models.Trip, error)
}

// AdminTripsByOrdersHandlers serves GET /admin/trips/by-orders and
// GET /admin/orders/{orderId}/live-trip.
// Mount behind RequireAdminAuth (parent router applies auth).
type AdminTripsByOrdersHandlers struct {
	lister tripByOrderLister
	live   liveTripGetter
	logger *logrus.Logger
}

func NewAdminTripsByOrdersHandlers(repo *repository.TripRepository, logger *logrus.Logger) *AdminTripsByOrdersHandlers {
	return &AdminTripsByOrdersHandlers{lister: repo, live: repo, logger: logger}
}

func newAdminTripsByOrdersHandlers(lister tripByOrderLister, logger *logrus.Logger) *AdminTripsByOrdersHandlers {
	return &AdminTripsByOrdersHandlers{lister: lister, logger: logger}
}

type tripsByOrderItem struct {
	OrderID string `json:"order_id"`
	// DistanceKM is this order's own km. A batched tour total is not reused
	// for every stop; nil when a batched order has no solo distance on record.
	DistanceKM *float64 `json:"distance_km,omitempty"`
	ReachedAt  string   `json:"reached_at,omitempty"`
	TripStatus string   `json:"trip_status"`
	// Batched is true when this order rode on a multi-order trip.
	Batched bool `json:"batched,omitempty"`
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
			OrderID:    id,
			DistanceKM: orderDistanceKM(trips, chosen, id),
			TripStatus: string(chosen.Status),
			Batched:    tripIsBatched(chosen),
		}
		if drop := dropForOrderLeg(chosen, id); drop != nil {
			item.ReachedAt = drop.ReachedAt
		}
		out = append(out, item)
	}
	h.respondWithJSON(w, http.StatusOK, tripsByOrdersResponse{Trips: out})
}

// tripIsBatched reports a trip that carries more than one order.
func tripIsBatched(trip *models.Trip) bool {
	return trip != nil && len(trip.LiveOrderIDs()) > 1
}

// orderDistanceKM is the km for this order. A single-order trip keeps its
// frozen distance. A batched tour does not: the order's own km is the latest
// single-order trip (usually the solo trip merged into the batch).
func orderDistanceKM(trips []*models.Trip, chosen *models.Trip, orderID string) *float64 {
	if chosen == nil {
		return nil
	}
	if !tripIsBatched(chosen) {
		km := chosen.DistanceKM
		return &km
	}
	var best *models.Trip
	for _, t := range trips {
		if !soloOrderTrip(t, orderID) {
			continue
		}
		if best == nil || t.CreatedAt > best.CreatedAt {
			best = t
		}
	}
	if best == nil {
		return nil
	}
	km := best.DistanceKM
	return &km
}

func soloOrderTrip(t *models.Trip, orderID string) bool {
	if t == nil || orderID == "" {
		return false
	}
	switch t.Status {
	case models.TripStatusCancelled, models.TripStatusDistanceFailed:
		return false
	}
	ids := t.LiveOrderIDs()
	return len(ids) == 1 && ids[0] == orderID
}

// dropForOrderLeg is this order's drop. A batched trip must not fall back to
// the first drop, which belongs to a different stop. A single-order trip
// written before tasks carried order_id still resolves through DropTask.
func dropForOrderLeg(trip *models.Trip, orderID string) *models.Task {
	if trip == nil {
		return nil
	}
	if drop := trip.DropForOrder(orderID); drop != nil {
		return drop
	}
	if tripIsBatched(trip) {
		return nil
	}
	return trip.DropTask()
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
