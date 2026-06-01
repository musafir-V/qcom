package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

type DistanceService struct {
	apiKey     string
	httpClient *http.Client
	logger     *logrus.Logger
}

func NewDistanceService(apiKey string, logger *logrus.Logger) *DistanceService {
	return &DistanceService{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		logger: logger,
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

	url := fmt.Sprintf(
		"https://maps.googleapis.com/maps/api/distancematrix/json?origins=%.6f%%2C%.6f&destinations=%.6f%%2C%.6f&key=%s",
		originLat, originLng, destLat, destLng, s.apiKey,
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
		return 0, op.Fail(fmt.Errorf("distance API status: %s", result.Status))
	}
	if len(result.Rows) == 0 || len(result.Rows[0].Elements) == 0 {
		return 0, op.Fail(fmt.Errorf("distance API returned empty result"))
	}

	elem := result.Rows[0].Elements[0]
	if elem.Status != "OK" {
		return 0, op.Fail(fmt.Errorf("distance element status: %s", elem.Status))
	}

	km := float64(elem.Distance.Value) / 1000.0
	op.With("km", km)
	return km, nil
}
