package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/metrics"
	"github.com/sirupsen/logrus"
)

// ErrNoRoute is returned by DistanceKM when the Distance Matrix API reports that
// no drivable route exists between the two points (status ZERO_RESULTS or
// NOT_FOUND), at either the top level or the element level. This is a permanent
// condition for the given coordinates: retrying will only re-bill the same call,
// so callers MUST treat it as terminal (do not retry) — see errors.Is checks in
// the assignment cron.
var ErrNoRoute = errors.New("distance: no drivable route between points")

// distanceMatrixBaseURL is the Google Maps Distance Matrix JSON endpoint.
const distanceMatrixBaseURL = "https://maps.googleapis.com/maps/api/distancematrix/json"

type DistanceService struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	logger     *logrus.Logger
}

func NewDistanceService(apiKey string, logger *logrus.Logger) *DistanceService {
	return &DistanceService{
		apiKey:     apiKey,
		baseURL:    distanceMatrixBaseURL,
		httpClient: metrics.NewInstrumentedClient("google_distance", 5*time.Second),
		logger:     logger,
	}
}

// DistanceKM returns the road distance in kilometres between two lat/lng points
// using the Google Maps Distance Matrix API.
// Returns an error if the API call fails; callers should skip trip creation and retry next tick.
func (s *DistanceService) DistanceKM(ctx context.Context, originLat, originLng, destLat, destLng float64) (float64, error) {
	op := logging.Start(ctx, s.logger, "DistanceService.DistanceKM", logrus.Fields{
		"origin": fmt.Sprintf("%.4f,%.4f", originLat, originLng),
		"dest":   fmt.Sprintf("%.4f,%.4f", destLat, destLng),
	})
	defer op.End()

	base := s.baseURL
	if base == "" {
		base = distanceMatrixBaseURL
	}
	url := fmt.Sprintf(
		"%s?origins=%.6f%%2C%.6f&destinations=%.6f%%2C%.6f&key=%s",
		base, originLat, originLng, destLat, destLng, s.apiKey,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, op.Fail(fmt.Errorf("failed to build distance request: %w", err))
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, op.Fail(fmt.Errorf("distance API unavailable: %w", err))
	}
	defer resp.Body.Close()

	var result struct {
		Status string `json:"status"`
		Rows   []struct {
			Elements []struct {
				Status   string `json:"status"`
				Distance struct {
					Value int `json:"value"` // metres
				} `json:"distance"`
			} `json:"elements"`
		} `json:"rows"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, op.Fail(fmt.Errorf("failed to decode distance response: %w", err))
	}

	if result.Status != "OK" {
		if isNoRouteStatus(result.Status) {
			return 0, op.Fail(fmt.Errorf("distance API status %s: %w", result.Status, ErrNoRoute))
		}
		return 0, op.Fail(fmt.Errorf("distance API status: %s", result.Status))
	}
	if len(result.Rows) == 0 || len(result.Rows[0].Elements) == 0 {
		return 0, op.Fail(fmt.Errorf("distance API returned empty result"))
	}

	elem := result.Rows[0].Elements[0]
	if elem.Status != "OK" {
		if isNoRouteStatus(elem.Status) {
			return 0, op.Fail(fmt.Errorf("distance element status %s: %w", elem.Status, ErrNoRoute))
		}
		return 0, op.Fail(fmt.Errorf("distance element status: %s", elem.Status))
	}

	km := float64(elem.Distance.Value) / 1000.0
	op.With("km", km)
	return km, nil
}

// isNoRouteStatus reports whether a Distance Matrix status string means the pair
// of coordinates has no drivable route — a permanent condition that callers must
// not retry. ZERO_RESULTS: no route found (e.g. intercontinental). NOT_FOUND: an
// endpoint could not be geocoded. Every other non-OK status (OVER_QUERY_LIMIT,
// UNKNOWN_ERROR, etc.) is transient and remains a retryable error.
func isNoRouteStatus(status string) bool {
	return status == "ZERO_RESULTS" || status == "NOT_FOUND"
}
