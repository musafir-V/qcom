package service

import (
	"context"

	"github.com/qcom/qcom/internal/models"
)

type distancer interface {
	DistanceKM(ctx context.Context, originLat, originLng, destLat, destLng float64) (float64, error)
}

type tourResult struct {
	Drops     []models.Task
	KM        float64
	Method    string
	Haversine bool
}

// nearestNeighbourTour reorders open drops. On any Google error the whole tour
// switches to haversine. pinFirst, when non-empty task_id, stays first and is
// the origin for the remaining drops (no store→pinFirst leg in tour km).
func nearestNeighbourTour(
	ctx context.Context,
	d distancer,
	originLat, originLng float64,
	open []models.Task,
	pinFirstTaskID string,
) tourResult {
	if len(open) == 0 {
		return tourResult{Method: models.DistanceMethodGoogle}
	}

	res, err := nnOnce(ctx, d, originLat, originLng, open, pinFirstTaskID, false)
	if err == nil {
		res.Method = models.DistanceMethodGoogle
		return res
	}
	res, _ = nnOnce(ctx, d, originLat, originLng, open, pinFirstTaskID, true)
	res.Method = models.DistanceMethodHaversine
	res.Haversine = true
	return res
}

func nnOnce(
	ctx context.Context,
	d distancer,
	originLat, originLng float64,
	open []models.Task,
	pinFirstTaskID string,
	haversine bool,
) (tourResult, error) {
	remaining := append([]models.Task(nil), open...)
	ordered := make([]models.Task, 0, len(remaining))
	lat, lng := originLat, originLng
	km := 0.0

	if pinFirstTaskID != "" {
		for i, t := range remaining {
			if t.TaskID == pinFirstTaskID {
				ordered = append(ordered, t)
				lat, lng = t.Lat, t.Lng
				remaining = append(remaining[:i], remaining[i+1:]...)
				break
			}
		}
	}

	for len(remaining) > 0 {
		best := 0
		bestKM := -1.0
		for i, t := range remaining {
			leg, err := legKM(ctx, d, lat, lng, t.Lat, t.Lng, haversine)
			if err != nil {
				return tourResult{}, err
			}
			if bestKM < 0 || leg < bestKM {
				bestKM = leg
				best = i
			}
		}
		km += bestKM
		next := remaining[best]
		ordered = append(ordered, next)
		lat, lng = next.Lat, next.Lng
		remaining = append(remaining[:best], remaining[best+1:]...)
	}
	return tourResult{Drops: ordered, KM: km}, nil
}

func legKM(ctx context.Context, d distancer, oLat, oLng, dLat, dLng float64, haversine bool) (float64, error) {
	if haversine || d == nil {
		return models.HaversineDistance(oLat, oLng, dLat, dLng) / 1000.0, nil
	}
	return d.DistanceKM(ctx, oLat, oLng, dLat, dLng)
}

func copyTask(t models.Task) models.Task {
	c := t
	c.TaskID = ""
	if t.Payment != nil {
		p := *t.Payment
		c.Payment = &p
	}
	if t.DropDeadline != nil {
		d := *t.DropDeadline
		c.DropDeadline = &d
	}
	if len(t.Items) > 0 {
		c.Items = append([]models.TripItem(nil), t.Items...)
	}
	return c
}

func stampTaskOrder(t *models.Task, trip *models.Trip) {
	if t.OrderID == "" && trip != nil {
		t.OrderID = trip.OrderID
	}
	if t.Type == models.TaskTypePickup && len(t.Items) == 0 && trip != nil {
		t.Items = append([]models.TripItem(nil), trip.Items...)
	}
	if t.Type == models.TaskTypeDrop && t.Payment == nil && trip != nil {
		t.Payment = trip.Payment
	}
	if t.Type == models.TaskTypeDrop && t.DropDeadline == nil && trip != nil {
		t.DropDeadline = trip.DropDeadline
	}
}

func resetReachedIfIncompletePickups(tasks []models.Task) {
	hasOpenPickup := false
	for i := range tasks {
		if tasks[i].Type == models.TaskTypePickup && tasks[i].Status != models.TaskStatusCompleted {
			hasOpenPickup = true
			break
		}
	}
	if !hasOpenPickup {
		return
	}
	for i := range tasks {
		if tasks[i].Type != models.TaskTypeDrop || tasks[i].Status == models.TaskStatusCompleted {
			continue
		}
		tasks[i].Status = models.TaskStatusCreated
		tasks[i].ReachedAt = ""
	}
}

func latestCompletedDrop(tasks []models.Task) *models.Task {
	var best *models.Task
	for i := range tasks {
		t := &tasks[i]
		if t.Type != models.TaskTypeDrop || t.Status != models.TaskStatusCompleted {
			continue
		}
		if best == nil || t.CompletedAt > best.CompletedAt {
			best = t
		}
	}
	return best
}

func firstOpenDrop(tasks []models.Task) *models.Task {
	for i := range tasks {
		if tasks[i].Type == models.TaskTypeDrop && tasks[i].Status != models.TaskStatusCompleted {
			return &tasks[i]
		}
	}
	return nil
}

func remainingCOD(tasks []models.Task) float64 {
	sum := 0.0
	for i := range tasks {
		t := &tasks[i]
		if t.Type != models.TaskTypeDrop || t.Status == models.TaskStatusCompleted || t.Payment == nil {
			continue
		}
		if t.Payment.CollectCash {
			sum += t.Payment.AmountZMW
		}
	}
	return sum
}
