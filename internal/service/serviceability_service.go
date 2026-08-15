package service

import (
	"context"
	"strings"

	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

const (
	serviceableAddressRadiusMeters = 50.0
	testModeETAMinutes             = 7

	sourceSavedAddress = "saved_address"
	sourceGeocoded     = "geocoded"

	ReasonOutsideDeliveryZone = "outside_delivery_zone"
	ReasonStoreInactive       = "store_inactive"
	ReasonStoreClosed         = "store_closed"

	// bypassDarkstoreID is the fixed dummy store returned for allowlisted users
	// (SERVICEABILITY_BYPASS_USER_IDS) whose polygon check is skipped.
	bypassDarkstoreID = "100"
	// bypassETAMinutes is the hardcoded ETA returned on the bypass path (there
	// is no real darkstore behind bypassDarkstoreID to compute one from).
	bypassETAMinutes = 7
)

// OperatingHours is the daily schedule surfaced to the customer app.
type OperatingHours struct {
	OpensAt  string `json:"opens_at"`
	ClosesAt string `json:"closes_at"`
	Timezone string `json:"timezone"`
}

// ResolvedAddress is the address surfaced to the customer on a serviceable location.
type ResolvedAddress struct {
	AddressLine string  `json:"address_line"`
	Tag         *string `json:"tag"`
	AddressID   *string `json:"address_id"`
	Source      string  `json:"source"`
}

// ServiceabilityResult is the outcome of a serviceability check.
type ServiceabilityResult struct {
	Serviceable     bool              `json:"serviceable"`
	Reason          string            `json:"reason,omitempty"`
	DarkstoreID     string            `json:"darkstore_id,omitempty"`
	IsOperational   *bool             `json:"is_operational,omitempty"`
	OperatingHours  *OperatingHours   `json:"operating_hours,omitempty"`
	NextOpensAt     string            `json:"next_opens_at,omitempty"`
	ResolvedAddress *ResolvedAddress  `json:"resolved_address,omitempty"`
	ETAMinutes      *int              `json:"eta_minutes,omitempty"`
}

type ServiceabilityService struct {
	darkstoreRepo  *repository.DarkstoreRepository
	addressService *AddressService
	geocoder       Geocoder
	etaService     ETAProvider
	logger         *logrus.Logger
	isTest         bool
	// bypassUserIDs is the set of JWT entity_ids that skip the polygon check and
	// always receive a serviceable result for bypassDarkstoreID. Lookups are
	// case-sensitive (keys are stored verbatim).
	bypassUserIDs map[string]struct{}
}

func NewServiceabilityService(
	darkstoreRepo *repository.DarkstoreRepository,
	addressService *AddressService,
	geocoder Geocoder,
	etaService ETAProvider,
	logger *logrus.Logger,
	isTest bool,
	bypassUserIDs []string,
) *ServiceabilityService {
	bypass := make(map[string]struct{}, len(bypassUserIDs))
	for _, id := range bypassUserIDs {
		if id != "" {
			bypass[id] = struct{}{}
		}
	}
	return &ServiceabilityService{
		darkstoreRepo:  darkstoreRepo,
		addressService: addressService,
		geocoder:       geocoder,
		etaService:     etaService,
		logger:         logger,
		isTest:         isTest,
		bypassUserIDs:  bypass,
	}
}

// isBypassUser reports whether userID is in the serviceability bypass allowlist.
// Empty user ids (e.g. guests) never match.
func (s *ServiceabilityService) isBypassUser(userID string) bool {
	if userID == "" {
		return false
	}
	_, ok := s.bypassUserIDs[userID]
	return ok
}

// newBypassResult builds the base synthetic serviceable result for an
// allowlisted user: always serviceable, fixed dummy store, operational, with a
// hardcoded ETA. The resolved address is filled in separately by the caller so
// this stays a pure, dependency-free builder (used directly by unit tests).
func newBypassResult() *ServiceabilityResult {
	isOp := true
	eta := bypassETAMinutes
	return &ServiceabilityResult{
		Serviceable:   true,
		DarkstoreID:   bypassDarkstoreID,
		IsOperational: &isOp,
		ETAMinutes:    &eta,
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

	// Allowlisted users bypass the polygon check entirely and always get a
	// serviceable result for the fixed dummy store. Runs before any darkstore
	// lookup so it is independent of DDB / IS_TEST. The address is still
	// resolved through the normal saved-address -> geocode path.
	if s.isBypassUser(userID) {
		op.Logger().Warn("user in serviceability bypass allowlist; skipping polygon check")
		op.With("serviceable", true).
			With("darkstore_id", bypassDarkstoreID).
			With("bypass", true)
		result := newBypassResult()

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

	darkstores, err := s.darkstoreRepo.ListAll(ctx)
	if err != nil {
		return nil, op.Fail(err)
	}

	// Store 100 is the synthetic bypass dummy; non-allowlisted users must never
	// match it, including under IS_TEST where matchDarkstore picks darkstores[0].
	darkstores = excludeDarkstoreID(darkstores, bypassDarkstoreID)

	matched := s.matchDarkstore(op, darkstores, lat, lng)
	if matched == nil {
		op.With("serviceable", false).With("reason", ReasonOutsideDeliveryZone)
		return &ServiceabilityResult{
			Serviceable: false,
			Reason:      ReasonOutsideDeliveryZone,
		}, nil
	}

	if !matched.IsActive || !matched.ValidOperatingHours() {
		op.With("serviceable", false).
			With("reason", ReasonStoreInactive).
			With("darkstore_id", matched.DarkstoreID)
		return &ServiceabilityResult{
			Serviceable: false,
			Reason:      ReasonStoreInactive,
			DarkstoreID: matched.DarkstoreID,
		}, nil
	}

	now := timezone.Now()
	hours := operatingHoursFromDarkstore(matched)
	isOperational := matched.IsOperationalAt(now)

	if !isOperational {
		if _, ok := matched.NextOpensAt(now); !ok {
			op.With("serviceable", false).
				With("reason", ReasonStoreInactive).
				With("darkstore_id", matched.DarkstoreID)
			return &ServiceabilityResult{
				Serviceable: false,
				Reason:      ReasonStoreInactive,
				DarkstoreID: matched.DarkstoreID,
			}, nil
		}
		return s.storeClosedResult(op, matched), nil
	}

	op.With("serviceable", true).With("darkstore_id", matched.DarkstoreID)
	isOp := true
	result := &ServiceabilityResult{
		Serviceable:    true,
		DarkstoreID:    matched.DarkstoreID,
		IsOperational:  &isOp,
		OperatingHours: hours,
	}

	if s.isTest {
		op.Logger().Warn("IS_TEST/IS_TRUE is set; using hardcoded ETA")
		testETA := testModeETAMinutes
		result.ETAMinutes = &testETA
	} else {
		etaMinutes, err := s.etaService.GetETA(ctx, matched, lat, lng)
		if err != nil {
			op.Logger().WithError(err).Warn("ETA calculation failed; returning serviceable result without ETA")
		} else {
			result.ETAMinutes = &etaMinutes
		}
	}

	if userID != "" {
		resolved, err := s.resolveFromSavedAddress(ctx, userID, lat, lng)
		if err != nil {
			return nil, op.Fail(err)
		}
		if resolved != nil {
			result.ResolvedAddress = resolved
			return result, nil
		}
	}

	result.ResolvedAddress = s.resolveFromGeocode(ctx, lat, lng)
	return result, nil
}

func operatingHoursFromDarkstore(ds *models.Darkstore) *OperatingHours {
	return &OperatingHours{
		OpensAt:  ds.OpensAt,
		ClosesAt: ds.ClosesAt,
		Timezone: models.OperatingHoursTimezone,
	}
}

func (s *ServiceabilityService) storeClosedResult(op *logging.Op, ds *models.Darkstore) *ServiceabilityResult {
	nextOpensAt, ok := ds.NextOpensAt(timezone.Now())
	if !ok {
		op.With("serviceable", false).
			With("reason", ReasonStoreInactive).
			With("darkstore_id", ds.DarkstoreID)
		return &ServiceabilityResult{
			Serviceable: false,
			Reason:      ReasonStoreInactive,
			DarkstoreID: ds.DarkstoreID,
		}
	}

	isOp := false
	op.With("serviceable", false).
		With("reason", ReasonStoreClosed).
		With("darkstore_id", ds.DarkstoreID)
	return &ServiceabilityResult{
		Serviceable:    false,
		Reason:         ReasonStoreClosed,
		DarkstoreID:    ds.DarkstoreID,
		IsOperational:  &isOp,
		OperatingHours: operatingHoursFromDarkstore(ds),
		NextOpensAt:    nextOpensAt,
	}
}

// excludeDarkstoreID returns a copy of stores with the given ID removed.
func excludeDarkstoreID(stores []models.Darkstore, id string) []models.Darkstore {
	filtered := make([]models.Darkstore, 0, len(stores))
	for _, ds := range stores {
		if ds.DarkstoreID != id {
			filtered = append(filtered, ds)
		}
	}
	return filtered
}

// matchDarkstore picks the darkstore for this request. When IS_TEST/IS_TRUE is set,
// polygon checks are skipped: the first darkstore from DDB is used and the
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
	// are skipped so the result is never ", , Foo" or "Foo, , ".
	line := joinNonEmpty(", ", nearest.BuildingAndFloor, nearest.AddressLine1, nearest.AddressLine2)
	tag := nearest.Tag
	id := nearest.AddressID
	return &ResolvedAddress{
		AddressLine: line,
		Tag:         &tag,
		AddressID:   &id,
		Source:      sourceSavedAddress,
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
