package service

import (
	"context"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

const (
	serviceableAddressRadiusMeters = 50.0

	sourceSavedAddress = "saved_address"
	sourceGeocoded     = "geocoded"
)

// ResolvedAddress is the address surfaced to the customer on a serviceable location.
type ResolvedAddress struct {
	AddressLine string  `json:"address_line"`
	Tag         *string `json:"tag"`
	AddressID   *string `json:"address_id"`
	Source      string  `json:"source"`
}

// ServiceabilityResult is the outcome of a serviceability check.
type ServiceabilityResult struct {
	Serviceable     bool             `json:"serviceable"`
	DarkstoreID     string           `json:"darkstore_id,omitempty"`
	ResolvedAddress *ResolvedAddress `json:"resolved_address,omitempty"`
}

type ServiceabilityService struct {
	darkstoreRepo  *repository.DarkstoreRepository
	addressService *AddressService
	geocoder       Geocoder
	logger         *logrus.Logger
}

func NewServiceabilityService(
	darkstoreRepo *repository.DarkstoreRepository,
	addressService *AddressService,
	geocoder Geocoder,
	logger *logrus.Logger,
) *ServiceabilityService {
	return &ServiceabilityService{
		darkstoreRepo:  darkstoreRepo,
		addressService: addressService,
		geocoder:       geocoder,
		logger:         logger,
	}
}

// CheckServiceability determines whether a coordinate is serviceable and, if so,
// resolves an address for it — either a nearby saved address or a geocoded one.
func (s *ServiceabilityService) CheckServiceability(ctx context.Context, userID string, lat, lng float64) (*ServiceabilityResult, error) {
	darkstores, err := s.darkstoreRepo.ListActive(ctx)
	if err != nil {
		return nil, err
	}

	// Polygons are non-overlapping, so the first containing polygon wins.
	var matched *models.Darkstore
	for i := range darkstores {
		if darkstores[i].Contains(lat, lng) {
			matched = &darkstores[i]
			break
		}
	}

	if matched == nil {
		return &ServiceabilityResult{Serviceable: false}, nil
	}

	result := &ServiceabilityResult{
		Serviceable: true,
		DarkstoreID: matched.DarkstoreID,
	}

	resolved, err := s.resolveFromSavedAddress(ctx, userID, lat, lng)
	if err != nil {
		return nil, err
	}
	if resolved != nil {
		result.ResolvedAddress = resolved
		return result, nil
	}

	result.ResolvedAddress = s.resolveFromGeocode(ctx, lat, lng)
	return result, nil
}

// resolveFromSavedAddress returns the nearest saved address within 50 m, or nil.
func (s *ServiceabilityService) resolveFromSavedAddress(ctx context.Context, userID string, lat, lng float64) (*ResolvedAddress, error) {
	addresses, err := s.addressService.GetMyAddresses(ctx, userID)
	if err != nil {
		return nil, err
	}

	var nearest *models.Address
	nearestDist := serviceableAddressRadiusMeters
	for i := range addresses {
		dist := models.HaversineDistance(lat, lng, addresses[i].Latitude, addresses[i].Longitude)
		if dist <= nearestDist {
			nearestDist = dist
			nearest = &addresses[i]
		}
	}

	if nearest == nil {
		return nil, nil
	}

	line := nearest.AddressLine2
	if line == "" {
		line = nearest.AddressLine1
	}
	tag := nearest.Label
	id := nearest.AddressID
	return &ResolvedAddress{
		AddressLine: line,
		Tag:         &tag,
		AddressID:   &id,
		Source:      sourceSavedAddress,
	}, nil
}

// resolveFromGeocode reverse-geocodes the coordinate. A geocoding failure is not
// fatal — the location is still serviceable; we just return no resolved address.
func (s *ServiceabilityService) resolveFromGeocode(ctx context.Context, lat, lng float64) *ResolvedAddress {
	line, err := s.geocoder.ReverseGeocode(ctx, lat, lng)
	if err != nil {
		s.logger.WithError(err).Warn("Reverse geocoding failed; returning serviceable result without a resolved address")
		return nil
	}
	return &ResolvedAddress{
		AddressLine: line,
		Source:      sourceGeocoded,
	}
}
