package service

import (
	"context"
	"fmt"
	"time"

	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

// maxScanAccuracyMeters rejects a presence scan whose GPS accuracy circle is
// wider than this — the fix is too coarse to trust against the tight geofence.
const maxScanAccuracyMeters = 150.0

type DEService struct {
	deRepo             *repository.DERepository
	qrService          *QRService
	referralService    *ReferralService
	earningsLedgerRepo deEarningsLedgerReader
	cashConfigRepo     *repository.CashConfigRepository
	darkstoreRepo      *repository.DarkstoreRepository
	statusEventRepo    *repository.DEStatusEventRepository
	logger             *logrus.Logger
}

type deEarningsLedgerReader interface {
	SumPositiveCashByDEAfter(ctx context.Context, deID, afterTimestamp string) (float64, error)
}

func NewDEService(deRepo *repository.DERepository, qrService *QRService, referralService *ReferralService, earningsLedgerRepo deEarningsLedgerReader, cashConfigRepo *repository.CashConfigRepository, darkstoreRepo *repository.DarkstoreRepository, statusEventRepo *repository.DEStatusEventRepository, logger *logrus.Logger) *DEService {
	return &DEService{deRepo: deRepo, qrService: qrService, referralService: referralService, earningsLedgerRepo: earningsLedgerRepo, cashConfigRepo: cashConfigRepo, darkstoreRepo: darkstoreRepo, statusEventRepo: statusEventRepo, logger: logger}
}

type RegisterDERequest struct {
	PhoneNumber       string
	Name              string
	ProfileURL        string
	NRCURL            string
	DriverLicenseURL  string
	NRCNumber         string
	AirtelMoneyNumber string
	BikeNumber        string
	BikeBrand         string
	ReferralCode      string // optional — code of the DE that referred this one
}

func (s *DEService) Register(ctx context.Context, req RegisterDERequest) (*models.DeliveryExecutive, error) {
	op := logging.Start(ctx, s.logger, "Register", logrus.Fields{"phone": req.PhoneNumber})
	defer op.End()

	// Generate a unique referral code for this new DE
	referralCode, err := s.referralService.GenerateUniqueCode(ctx)
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to generate referral code: %w", err))
	}

	de := &models.DeliveryExecutive{
		PhoneNumber:       req.PhoneNumber,
		Name:              req.Name,
		ProfileURL:        req.ProfileURL,
		NRCURL:            req.NRCURL,
		DriverLicenseURL:  req.DriverLicenseURL,
		NRCNumber:         req.NRCNumber,
		AirtelMoneyNumber: req.AirtelMoneyNumber,
		BikeNumber:        req.BikeNumber,
		BikeBrand:         req.BikeBrand,
		Status:            models.DEStatusOffline,
		ReferralCode:      referralCode,
	}

	if err := s.deRepo.Create(ctx, de); err != nil {
		return nil, op.Fail(err)
	}

	// Link referral if a code was provided — non-fatal if invalid
	if req.ReferralCode != "" {
		if linkErr := s.referralService.LinkReferral(ctx, de.DEID, de.Name, req.ReferralCode); linkErr != nil {
			s.logger.WithError(linkErr).Warn("referral linking failed during registration — continuing")
		}
	}

	return de, nil
}

func (s *DEService) GetDE(ctx context.Context, phone string) (*models.DeliveryExecutive, error) {
	op := logging.Start(ctx, s.logger, "GetDE", logrus.Fields{"phone": phone})
	defer op.End()

	de, err := s.deRepo.GetByPhone(ctx, phone)
	if err != nil {
		return nil, op.Fail(err)
	}
	if de == nil {
		return nil, op.Outcome("not_found", fmt.Errorf("delivery executive not found"))
	}
	return de, nil
}

// GetTodayEarnings returns the sum of the DE's earnings ledger entries since
// midnight Zambia time.
func (s *DEService) GetTodayEarnings(ctx context.Context, deID string) (float64, error) {
	return s.earningsLedgerRepo.SumPositiveCashByDEAfter(ctx, deID, timezone.StartOfDayString())
}

// ScanLocation is the foreground GPS fix + anti-spoof signal the driver app
// sends with a duty-start scan.
type ScanLocation struct {
	Lat       float64
	Lng       float64
	AccuracyM float64
	IsMocked  bool
}

// StartDuty validates the store QR + a geofenced presence scan and transitions
// the DE to eligible. Valid from: offline or free. On success it stamps the
// next scan deadline (now + ScanInterval), the last-scan location, and appends
// a status event (scan_start from offline, scan_return from free).
func (s *DEService) StartDuty(ctx context.Context, dePhone, qrCode string, loc ScanLocation) (string, error) {
	op := logging.Start(ctx, s.logger, "StartDuty", logrus.Fields{"phone": dePhone})
	defer op.End()

	de, err := s.deRepo.GetByPhone(ctx, dePhone)
	if err != nil {
		return "", op.Fail(fmt.Errorf("failed to fetch DE: %w", err))
	}
	if de == nil {
		return "", op.Outcome("not_found", fmt.Errorf("delivery executive not found"))
	}

	if de.Status == models.DEStatusBusy {
		return "", op.Outcome("busy", fmt.Errorf("cannot start duty while on an active delivery"))
	}
	if de.Status == models.DEStatusEligible {
		return "", op.Outcome("already_on_duty", fmt.Errorf("already on duty at store %s", de.CurrentStoreID))
	}

	cfg, err := s.cashConfigRepo.Get(ctx)
	if err != nil {
		return "", op.Fail(fmt.Errorf("failed to fetch cash config: %w", err))
	}
	if de.CashExceeds(cfg.EffectiveLimitZMW()) {
		return "", op.Outcome("cash_limit_exceeded", fmt.Errorf("in-hand cash limit exceeded; deposit cash to resume"))
	}

	storeID, err := s.qrService.ParseStoreID(qrCode)
	if err != nil {
		return "", op.Outcome("invalid_qr", fmt.Errorf("invalid QR code: %w", err))
	}

	if err := s.qrService.ValidateQRCode(qrCode, storeID); err != nil {
		return "", op.Fail(err)
	}

	// Anti-spoof + geofence. Log every rejection with coords for fraud review.
	if loc.IsMocked {
		s.logRejectedScan(op, dePhone, storeID, loc, "mocked_location")
		return "", op.Outcome("invalid_location", fmt.Errorf("location appears mocked; disable mock location to start duty"))
	}
	if loc.AccuracyM > maxScanAccuracyMeters {
		s.logRejectedScan(op, dePhone, storeID, loc, "inaccurate_location")
		return "", op.Outcome("location_inaccurate", fmt.Errorf("location accuracy too low; move outdoors and try again"))
	}

	ds, err := s.darkstoreRepo.GetByID(ctx, storeID)
	if err != nil {
		return "", op.Fail(fmt.Errorf("failed to fetch darkstore %s: %w", storeID, err))
	}
	if ds == nil {
		return "", op.Outcome("store_not_found", fmt.Errorf("store %s not found", storeID))
	}
	if !ds.WithinPresence(loc.Lat, loc.Lng, loc.AccuracyM) {
		s.logRejectedScan(op, dePhone, storeID, loc, "outside_geofence")
		return "", op.Outcome("outside_geofence", fmt.Errorf("outside store geofence; move closer to the store and try again"))
	}

	fromState := de.Status
	now := timezone.Now()
	nowUTC := now.UTC().Format(time.RFC3339)
	deadline := now.UTC().Add(models.ScanInterval).Format(time.RFC3339)

	if err := s.deRepo.MarkEligibleFromScan(ctx, dePhone, storeID, deadline, loc.Lat, loc.Lng, nowUTC); err != nil {
		return "", op.Fail(fmt.Errorf("failed to update DE status: %w", err))
	}

	reason := models.ReasonScanStart
	if fromState == models.DEStatusFree {
		reason = models.ReasonScanReturn
	}
	s.appendStatusEvent(ctx, &models.DEStatusEvent{
		Phone:     dePhone,
		FromState: fromState,
		ToState:   models.DEStatusEligible,
		Reason:    reason,
		StoreID:   storeID,
		Lat:       loc.Lat,
		Lng:       loc.Lng,
		AccuracyM: loc.AccuracyM,
		TS:        nowUTC,
	})

	op.With("store_id", storeID)
	return storeID, nil
}

// logRejectedScan records a failed presence scan for the fraud-review list.
func (s *DEService) logRejectedScan(op *logging.Op, phone, storeID string, loc ScanLocation, reason string) {
	op.Logger().WithFields(logrus.Fields{
		"phone":      phone,
		"store_id":   storeID,
		"reason":     reason,
		"lat":        loc.Lat,
		"lng":        loc.Lng,
		"accuracy_m": loc.AccuracyM,
		"is_mocked":  loc.IsMocked,
	}).Warn("presence scan rejected")
}

// appendStatusEvent writes a status-event log entry best-effort; a failure here
// must not fail the duty transition (the timeline is a reporting aid).
func (s *DEService) appendStatusEvent(ctx context.Context, event *models.DEStatusEvent) {
	if s.statusEventRepo == nil {
		return
	}
	if err := s.statusEventRepo.Append(ctx, event); err != nil {
		s.logger.WithError(err).WithField("phone", event.Phone).
			Warn("failed to append DE status event")
	}
}

// EndDuty transitions the DE from eligible or free to offline.
// Rejected if DE is busy (active trip in progress).
func (s *DEService) EndDuty(ctx context.Context, dePhone string) error {
	op := logging.Start(ctx, s.logger, "EndDuty", logrus.Fields{"phone": dePhone})
	defer op.End()

	de, err := s.deRepo.GetByPhone(ctx, dePhone)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to fetch DE: %w", err))
	}
	if de == nil {
		return op.Outcome("not_found", fmt.Errorf("delivery executive not found"))
	}
	if de.Status == models.DEStatusBusy {
		return op.Outcome("busy", fmt.Errorf("cannot end duty while on an active delivery"))
	}
	if de.Status == models.DEStatusOffline {
		return op.Outcome("already_offline", fmt.Errorf("already offline"))
	}

	fromState := de.Status
	if err := s.deRepo.UpdateStatus(ctx, dePhone, models.DEStatusOffline, "", ""); err != nil {
		return op.Fail(fmt.Errorf("failed to update DE status: %w", err))
	}

	s.appendStatusEvent(ctx, &models.DEStatusEvent{
		Phone:     dePhone,
		FromState: fromState,
		ToState:   models.DEStatusOffline,
		Reason:    models.ReasonEndedDuty,
		StoreID:   de.CurrentStoreID,
		TS:        timezone.Now().UTC().Format(time.RFC3339),
	})
	return nil
}
