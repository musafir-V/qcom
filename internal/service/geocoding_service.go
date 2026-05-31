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

	h3 "github.com/uber/h3-go/v4"

	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

// geocodeH3Resolution is the H3 resolution used to key the reverse-geocode
// cache. r=9 ≈ 170 m hex — small enough that "sublocality, locality" is
// constant inside a cell, large enough for a high hit rate. Distinct from
// h3Resolution (r=7) used by ETAService because the two cache different
// signals at different granularities.
const geocodeH3Resolution = 9

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

	op := logging.Start(ctx, g.logger, "ReverseGeocode", logrus.Fields{
		"lat": lat,
		"lng": lng,
	})
	defer op.End()

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return "", op.Fail(fmt.Errorf("geocode request failed: %w", err))
	}
	defer resp.Body.Close()
	op.With("status_code", resp.StatusCode)

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

// GeocodeCacheStore is the persistence contract used by CachedGeocoder for
// reverse-geocode look-ups and writes. GeocodeCacheRepository satisfies it.
type GeocodeCacheStore interface {
	Get(ctx context.Context, h3Cell string) (*string, error)
	Save(ctx context.Context, h3Cell string, address string) error
}

// CachedGeocoder decorates an inner Geocoder with an H3-cell-keyed cache.
// Flow: H3 cell → cache → inner geocoder → save → return.
//
// Cache failures never break the request: a Get error falls back to the inner
// geocoder, and a Save error is logged but the geocoded address is still
// returned. Inner-geocoder errors propagate unchanged so callers see the same
// behaviour as the bare geocoder.
type CachedGeocoder struct {
	inner  Geocoder
	cache  GeocodeCacheStore
	logger *logrus.Logger
}

// NewCachedGeocoder wraps inner with cache. inner must be non-nil; cache may
// be any GeocodeCacheStore implementation.
func NewCachedGeocoder(inner Geocoder, cache GeocodeCacheStore, logger *logrus.Logger) *CachedGeocoder {
	return &CachedGeocoder{inner: inner, cache: cache, logger: logger}
}

// ReverseGeocode returns a "Sublocality, Locality" string for the given
// coordinate, served from the H3-cell cache when available.
func (g *CachedGeocoder) ReverseGeocode(ctx context.Context, lat, lng float64) (string, error) {
	op := logging.Start(ctx, g.logger, "ReverseGeocode.Cached", logrus.Fields{
		"lat": lat,
		"lng": lng,
	})
	defer op.End()

	cellKey, err := geocodeCellKey(lat, lng)
	if err != nil {
		// H3 failure shouldn't sink the whole request — fall through to the
		// inner geocoder uncached. This branch is effectively unreachable for
		// valid coordinates.
		op.Logger().WithError(err).Warn("H3 cell computation failed; bypassing cache")
		return g.inner.ReverseGeocode(ctx, lat, lng)
	}
	op.With("h3_cell", cellKey)

	cached, err := g.cache.Get(ctx, cellKey)
	if err != nil {
		op.Logger().WithError(err).Warn("Geocode cache lookup failed; falling back to inner geocoder")
	} else if cached != nil {
		op.With("cache_hit", true)
		return *cached, nil
	}

	address, err := g.inner.ReverseGeocode(ctx, lat, lng)
	if err != nil {
		return "", err
	}

	if saveErr := g.cache.Save(ctx, cellKey, address); saveErr != nil {
		op.Logger().WithError(saveErr).Warn("Failed to cache geocode result; returning value")
	}

	op.With("cache_hit", false)
	return address, nil
}

// geocodeCellKey reduces a coordinate to its H3 r=9 cell, formatted as the
// canonical lowercase hex string used as the cache primary key.
func geocodeCellKey(lat, lng float64) (string, error) {
	cell, err := h3.LatLngToCell(h3.NewLatLng(lat, lng), geocodeH3Resolution)
	if err != nil {
		return "", fmt.Errorf("failed to compute H3 cell: %w", err)
	}
	return fmt.Sprintf("%x", uint64(cell)), nil
}
