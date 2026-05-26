package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

// ErrGeocoderNotConfigured is returned when no GOOGLE_MAPS_API_KEY is set.
var ErrGeocoderNotConfigured = errors.New("geocoder not configured: GOOGLE_MAPS_API_KEY is missing")

// ErrNoGeocodeResult is returned when Google has no usable result for a coordinate.
var ErrNoGeocodeResult = errors.New("no geocode result for coordinate")

// Geocoder resolves a coordinate to a short human-readable locality string.
type Geocoder interface {
	ReverseGeocode(ctx context.Context, lat, lng float64) (string, error)
}

const googleGeocodeURL = "https://maps.googleapis.com/maps/api/geocode/json"

type GoogleGeocoder struct {
	apiKey     string
	httpClient *http.Client
	logger     *logrus.Logger
}

func NewGoogleGeocoder(apiKey string, logger *logrus.Logger) *GoogleGeocoder {
	return &GoogleGeocoder{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		logger:     logger,
	}
}

type googleGeocodeResponse struct {
	Status       string `json:"status"`
	ErrorMessage string `json:"error_message"`
	Results      []struct {
		FormattedAddress  string `json:"formatted_address"`
		AddressComponents []struct {
			LongName string   `json:"long_name"`
			Types    []string `json:"types"`
		} `json:"address_components"`
	} `json:"results"`
}

// ReverseGeocode returns a "Sublocality, Locality" string (e.g. "Indiranagar, Bengaluru").
func (g *GoogleGeocoder) ReverseGeocode(ctx context.Context, lat, lng float64) (string, error) {
	if g.apiKey == "" {
		return "", ErrGeocoderNotConfigured
	}

	q := url.Values{}
	q.Set("latlng", strconv.FormatFloat(lat, 'f', -1, 64)+","+strconv.FormatFloat(lng, 'f', -1, 64))
	q.Set("key", g.apiKey)
	q.Set("result_type", "sublocality|locality")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleGeocodeURL+"?"+q.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("failed to build geocode request: %w", err)
	}

	log := logging.FromContext(ctx, g.logger)
	extStart := time.Now()
	log.WithFields(logrus.Fields{
		"op":  "ReverseGeocode",
		"lat": lat,
		"lng": lng,
	}).Info("google_geocode call start")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "ReverseGeocode",
			"duration_ms": time.Since(extStart).Milliseconds(),
		}).Error("google_geocode call failed")
		return "", fmt.Errorf("geocode request failed: %w", err)
	}
	defer resp.Body.Close()

	log.WithFields(logrus.Fields{
		"op":          "ReverseGeocode",
		"status_code": resp.StatusCode,
		"duration_ms": time.Since(extStart).Milliseconds(),
	}).Info("google_geocode call done")

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("geocode request returned HTTP %d", resp.StatusCode)
	}

	var body googleGeocodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("failed to decode geocode response: %w", err)
	}

	if body.Status != "OK" {
		if body.Status == "ZERO_RESULTS" {
			return "", ErrNoGeocodeResult
		}
		return "", fmt.Errorf("geocode API status %s: %s", body.Status, body.ErrorMessage)
	}

	line := extractAddressLine(body)
	if line == "" {
		return "", ErrNoGeocodeResult
	}
	return line, nil
}

// extractAddressLine picks the first sublocality and locality from the response
// and joins them, e.g. "Indiranagar, Bengaluru". Falls back to the first
// formatted address if no named components are present.
func extractAddressLine(body googleGeocodeResponse) string {
	var sublocality, locality string

	for _, result := range body.Results {
		for _, comp := range result.AddressComponents {
			for _, t := range comp.Types {
				switch t {
				case "sublocality", "sublocality_level_1":
					if sublocality == "" {
						sublocality = comp.LongName
					}
				case "locality":
					if locality == "" {
						locality = comp.LongName
					}
				}
			}
		}
		if sublocality != "" && locality != "" {
			break
		}
	}

	parts := make([]string, 0, 2)
	if sublocality != "" {
		parts = append(parts, sublocality)
	}
	if locality != "" {
		parts = append(parts, locality)
	}

	if len(parts) == 0 && len(body.Results) > 0 {
		return body.Results[0].FormattedAddress
	}
	return strings.Join(parts, ", ")
}
