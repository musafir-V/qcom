package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	h3 "github.com/uber/h3-go/v4"

	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/metrics"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

const (
	h3Resolution     = 7
	minutesPerKm     = 2.0
	packagingMinutes = 3

	googleDistanceMatrixURL = "https://maps.googleapis.com/maps/api/distancematrix/json"
)

// ETACacheStore is the persistence contract used by ETAService for cache look-ups
// and writes. ETACacheRepository satisfies this interface.
type ETACacheStore interface {
	Get(ctx context.Context, h3Cell string) (*int, error)
	Save(ctx context.Context, h3Cell string, etaMinutes int) error
}

// ETAProvider is the contract that ServiceabilityService depends on.
// ETAService satisfies it; tests supply a mock.
type ETAProvider interface {
	GetETA(ctx context.Context, darkstore *models.Darkstore, userLat, userLng float64) (int, error)
}

type ETAService struct {
	etaRepo               ETACacheStore
	apiKey                string
	httpClient            *http.Client
	logger                *logrus.Logger
	distanceMatrixBaseURL string // override in tests
}

func NewETAService(etaRepo ETACacheStore, apiKey string, logger *logrus.Logger) *ETAService {
	return &ETAService{
		etaRepo:               etaRepo,
		apiKey:                apiKey,
		httpClient:            metrics.NewInstrumentedClient("google_eta", 5*time.Second),
		logger:                logger,
		distanceMatrixBaseURL: googleDistanceMatrixURL,
	}
}

// GetETA returns estimated delivery time in minutes.
// Flow: H3 cell → DynamoDB cache → Google Distance Matrix → ceil(km×2)+3 → save → return.
func (s *ETAService) GetETA(ctx context.Context, darkstore *models.Darkstore, userLat, userLng float64) (int, error) {
	op := logging.Start(ctx, s.logger, "GetETA", logrus.Fields{
		"lat":          userLat,
		"lng":          userLng,
		"darkstore_id": darkstore.DarkstoreID,
	})
	defer op.End()

	cell, err := h3.LatLngToCell(h3.NewLatLng(userLat, userLng), h3Resolution)
	if err != nil {
		return 0, op.Fail(fmt.Errorf("failed to compute H3 cell: %w", err))
	}
	cellKey := fmt.Sprintf("%x", uint64(cell))

	cached, err := s.etaRepo.Get(ctx, cellKey)
	if err != nil {
		op.Logger().WithError(err).Warn("ETA cache lookup failed; falling back to Google")
	} else if cached != nil {
		op.With("cache_hit", true).With("eta_minutes", *cached)
		return *cached, nil
	}

	distanceMeters, err := s.fetchDistanceMeters(ctx, darkstore.Latitude, darkstore.Longitude, userLat, userLng)
	if err != nil {
		return 0, op.Fail(fmt.Errorf("failed to fetch distance from Google: %w", err))
	}

	distanceKm := float64(distanceMeters) / 1000.0
	etaMinutes := int(math.Ceil(distanceKm*minutesPerKm)) + packagingMinutes

	if saveErr := s.etaRepo.Save(ctx, cellKey, etaMinutes); saveErr != nil {
		op.Logger().WithError(saveErr).Warn("Failed to cache ETA; returning computed value")
	}

	op.With("cache_hit", false).With("eta_minutes", etaMinutes)
	return etaMinutes, nil
}

type distanceMatrixResponse struct {
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

func (s *ETAService) fetchDistanceMeters(ctx context.Context, originLat, originLng, destLat, destLng float64) (int, error) {
	if s.apiKey == "" {
		return 0, ErrGeocoderNotConfigured
	}

	q := url.Values{}
	q.Set("origins", formatLatLng(originLat, originLng))
	q.Set("destinations", formatLatLng(destLat, destLng))
	q.Set("key", s.apiKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.distanceMatrixBaseURL+"?"+q.Encode(), nil)
	if err != nil {
		return 0, fmt.Errorf("failed to build distance matrix request: %w", err)
	}

	op := logging.Start(ctx, s.logger, "fetchDistanceMeters", logrus.Fields{
		"origin":      formatLatLng(originLat, originLng),
		"destination": formatLatLng(destLat, destLng),
	})
	defer op.End()

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, op.Fail(fmt.Errorf("distance matrix request failed: %w", err))
	}
	defer resp.Body.Close()
	op.With("status_code", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("distance matrix returned HTTP %d", resp.StatusCode)
	}

	var body distanceMatrixResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("failed to decode distance matrix response: %w", err)
	}

	if body.Status != "OK" {
		return 0, fmt.Errorf("distance matrix API status: %s", body.Status)
	}
	if len(body.Rows) == 0 || len(body.Rows[0].Elements) == 0 {
		return 0, fmt.Errorf("distance matrix returned no elements")
	}

	elem := body.Rows[0].Elements[0]
	if elem.Status != "OK" {
		return 0, fmt.Errorf("distance matrix element status: %s", elem.Status)
	}

	return elem.Distance.Value, nil
}

func formatLatLng(lat, lng float64) string {
	return strconv.FormatFloat(lat, 'f', -1, 64) + "," + strconv.FormatFloat(lng, 'f', -1, 64)
}
