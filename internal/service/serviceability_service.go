package service

import (
	"context"
	"time"

	"github.com/qcom/qcom/internal/logging"
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
	ETAMinutes      *int             `json:"eta_minutes,omitempty"`
}

type ServiceabilityService struct {
	darkstoreRepo  *repository.DarkstoreRepository
	addressService *AddressService
	geocoder       Geocoder
	etaService     ETAProvider
	logger         *logrus.Logger
	isTest         bool
}

func NewServiceabilityService(
	darkstoreRepo *repository.DarkstoreRepository,
	addressService *AddressService,
	geocoder Geocoder,
	etaService ETAProvider,
	logger *logrus.Logger,
	isTest bool,
) *ServiceabilityService {
	return &ServiceabilityService{
		darkstoreRepo:  darkstoreRepo,
		addressService: addressService,
		geocoder:       geocoder,
		etaService:     etaService,
		logger:         logger,
		isTest:         isTest,
	}
}

// CheckServiceability determines whether a coordinate is serviceable and, if so,
// resolves an address for it — either a nearby saved address or a geocoded one.
func (s *ServiceabilityService) CheckServiceability(ctx context.Context, userID string, lat, lng float64) (*ServiceabilityResult, error) {
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":      "CheckServiceability",
		"user_id": userID,
		"lat":     lat,
		"lng":     lng,
	}).Info("service call start")

	darkstores, err := s.darkstoreRepo.ListActive(ctx)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "CheckServiceability",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return nil, err
	}

	var matched *models.Darkstore
	if s.isTest {
		if len(darkstores) == 0 {
			log.WithFields(logrus.Fields{
				"op":          "CheckServiceability",
				"duration_ms": time.Since(start).Milliseconds(),
				"serviceable": false,
			}).Info("service call done")
			return &ServiceabilityResult{Serviceable: false}, nil
		}
		matched = &darkstores[0]
		log.Warn("IS_TEST is set; bypassing serviceability polygon check")
	} else {
		for i := range darkstores {
			if darkstores[i].Contains(lat, lng) {
				matched = &darkstores[i]
				break
			}
		}
		if matched == nil {
			log.WithFields(logrus.Fields{
				"op":          "CheckServiceability",
				"duration_ms": time.Since(start).Milliseconds(),
				"serviceable": false,
			}).Info("service call done")
			return &ServiceabilityResult{Serviceable: false}, nil
		}
	}

	result := &ServiceabilityResult{
		Serviceable: true,
		DarkstoreID: matched.DarkstoreID,
	}

	etaMinutes, err := s.etaService.GetETA(ctx, matched, lat, lng)
	if err != nil {
		log.WithError(err).Warn("ETA calculation failed; returning serviceable result without ETA")
	} else {
		result.ETAMinutes = &etaMinutes
	}

	resolved, err := s.resolveFromSavedAddress(ctx, userID, lat, lng)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "CheckServiceability",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return nil, err
	}
	if resolved != nil {
		result.ResolvedAddress = resolved
		log.WithFields(logrus.Fields{
			"op":          "CheckServiceability",
			"duration_ms": time.Since(start).Milliseconds(),
			"serviceable": true,
		}).Info("service call done")
		return result, nil
	}

	result.ResolvedAddress = s.resolveFromGeocode(ctx, lat, lng)
	log.WithFields(logrus.Fields{
		"op":          "CheckServiceability",
		"duration_ms": time.Since(start).Milliseconds(),
		"serviceable": true,
	}).Info("service call done")
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
	log := logging.FromContext(ctx, s.logger)
	line, err := s.geocoder.ReverseGeocode(ctx, lat, lng)
	if err != nil {
		log.WithError(err).Warn("Reverse geocoding failed; returning serviceable result without a resolved address")
		return nil
	}
	return &ResolvedAddress{
		AddressLine: line,
		Source:      sourceGeocoded,
	}
}
