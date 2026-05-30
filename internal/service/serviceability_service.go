package service

import (
	"context"
	"strings"

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
//
// The structured fields (AddressLine1/AddressLine2/BuildingAndFloor/Receiver*/Lat/Lng)
// are populated only when Source == "saved_address". For Source == "geocoded" only
// AddressLine and Source are set; everything else is omitted from the JSON.
type ResolvedAddress struct {
	AddressLine string  `json:"address_line"`
	Tag         *string `json:"tag"`
	AddressID   *string `json:"address_id"`
	Source      string  `json:"source"`

	// Saved-address structured fields. omitempty keeps the geocoded response
	// shape unchanged — these only appear when we matched a saved address.
	AddressLine1     string   `json:"address_line_1,omitempty"`
	AddressLine2     string   `json:"address_line_2,omitempty"`
	BuildingAndFloor string   `json:"building_and_floor,omitempty"`
	ReceiverName     string   `json:"receiver_name,omitempty"`
	ReceiverPhone    string   `json:"receiver_phone,omitempty"`
	Latitude         *float64 `json:"latitude,omitempty"`
	Longitude        *float64 `json:"longitude,omitempty"`
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
	op := logging.Start(ctx, s.logger, "CheckServiceability", logrus.Fields{
		"user_id": userID,
		"lat":     lat,
		"lng":     lng,
	})
	defer op.End()

	darkstores, err := s.darkstoreRepo.ListActive(ctx)
	if err != nil {
		return nil, op.Fail(err)
	}

	matched := s.matchDarkstore(op, darkstores, lat, lng)
	if matched == nil {
		op.With("serviceable", false)
		return &ServiceabilityResult{Serviceable: false}, nil
	}

	op.With("serviceable", true).With("darkstore_id", matched.DarkstoreID)
	result := &ServiceabilityResult{
		Serviceable: true,
		DarkstoreID: matched.DarkstoreID,
	}

	etaMinutes, err := s.etaService.GetETA(ctx, matched, lat, lng)
	if err != nil {
		op.Logger().WithError(err).Warn("ETA calculation failed; returning serviceable result without ETA")
	} else {
		result.ETAMinutes = &etaMinutes
	}

	resolved, err := s.resolveFromSavedAddress(ctx, userID, lat, lng)
	if err != nil {
		return nil, op.Fail(err)
	}
	if resolved != nil {
		result.ResolvedAddress = resolved
		return result, nil
	}

	result.ResolvedAddress = s.resolveFromGeocode(ctx, lat, lng)
	return result, nil
}

// matchDarkstore picks the darkstore for this request. When IS_TEST/IS_TRUE is set,
// polygon checks are skipped: the first active darkstore from DDB is used and the
// rest of the flow (ETA, address resolution) proceeds as normal.
func (s *ServiceabilityService) matchDarkstore(op *logging.Op, darkstores []models.Darkstore, lat, lng float64) *models.Darkstore {
	if len(darkstores) == 0 {
		return nil
	}
	if s.isTest {
		op.Logger().Warn("IS_TEST/IS_TRUE is set; bypassing serviceability polygon check")
		return &darkstores[0]
	}
	for i := range darkstores {
		if darkstores[i].Contains(lat, lng) {
			return &darkstores[i]
		}
	}
	return nil
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

	// AddressLine is the full address as a single, comma-joined string:
	// "<building_and_floor>, <address_line_1>, <address_line_2>". Empty parts
	// are skipped so the result is never ", , Foo" or "Foo, , ". The structured
	// fields below are also returned so the client can render differently if
	// it wants.
	line := joinNonEmpty(", ", nearest.BuildingAndFloor, nearest.AddressLine1, nearest.AddressLine2)
	tag := nearest.Label
	id := nearest.AddressID
	addrLat := nearest.Latitude
	addrLng := nearest.Longitude
	return &ResolvedAddress{
		AddressLine:      line,
		Tag:              &tag,
		AddressID:        &id,
		Source:           sourceSavedAddress,
		AddressLine1:     nearest.AddressLine1,
		AddressLine2:     nearest.AddressLine2,
		BuildingAndFloor: nearest.BuildingAndFloor,
		ReceiverName:     nearest.ReceiverName,
		ReceiverPhone:    nearest.ReceiverPhone,
		Latitude:         &addrLat,
		Longitude:        &addrLng,
	}, nil
}

// joinNonEmpty joins the non-empty parts with sep. Used to build a display-friendly
// address line from heterogeneous fields without producing ", , Foo" style strings.
func joinNonEmpty(sep string, parts ...string) string {
	nonEmpty := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	return strings.Join(nonEmpty, sep)
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
