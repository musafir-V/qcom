package service

import (
	"context"
	"strings"
	"testing"

	"github.com/qcom/qcom/internal/models"
)

const returnScanStore = "221"

// storeLat/storeLng is the darkstore centre; scans at this point are inside the geofence.
const storeLat, storeLng = -15.4, 28.3

type scanDERepo struct {
	archiveDERepo
	eligibleCalls int
}

func (s *scanDERepo) MarkEligibleFromScan(context.Context, string, string, float64, float64, string) error {
	s.eligibleCalls++
	return nil
}

type stubCashConfig struct{}

func (stubCashConfig) Get(context.Context) (*models.CashConfig, error) {
	return &models.CashConfig{LimitZMW: 1000}, nil
}

type stubDarkstores struct{}

func (stubDarkstores) GetByID(_ context.Context, id string) (*models.Darkstore, error) {
	return &models.Darkstore{DarkstoreID: id, Latitude: storeLat, Longitude: storeLng, PresenceRadiusMeters: 100}, nil
}

type recordingReturnTrips struct {
	trips   map[string]*models.Trip
	stamped map[string]string
}

func (r *recordingReturnTrips) GetByID(_ context.Context, id string) (*models.Trip, error) {
	return r.trips[id], nil
}

func (r *recordingReturnTrips) StampReturnedAt(_ context.Context, id, ts string) (bool, error) {
	if r.stamped == nil {
		r.stamped = map[string]string{}
	}
	if _, ok := r.stamped[id]; ok {
		return false, nil
	}
	r.stamped[id] = ts
	return true, nil
}

type returnScanFixture struct {
	svc    *DEService
	deRepo *scanDERepo
	trips  *recordingReturnTrips
	events *recordingStatusEvents
	qr     string
}

func newReturnScanFixture(de *models.DeliveryExecutive, trips ...*models.Trip) *returnScanFixture {
	logger := testArchiveLogger()
	qr := NewQRService(logger)
	deRepo := &scanDERepo{archiveDERepo: archiveDERepo{de: de}}
	tripRepo := &recordingReturnTrips{trips: map[string]*models.Trip{}}
	for _, t := range trips {
		tripRepo.trips[t.TripID] = t
	}
	events := &recordingStatusEvents{}
	svc := &DEService{
		deRepo:          deRepo,
		qrService:       qr,
		cashConfigRepo:  stubCashConfig{},
		darkstoreRepo:   stubDarkstores{},
		statusEventRepo: events,
		tripRepo:        tripRepo,
		logger:          logger,
	}
	return &returnScanFixture{svc: svc, deRepo: deRepo, trips: tripRepo, events: events, qr: qr.GenerateQRCode(returnScanStore)}
}

func (f *returnScanFixture) scanAt(lat, lng float64) error {
	_, err := f.svc.StartDuty(context.Background(), "+260971000001", f.qr, ScanLocation{Lat: lat, Lng: lng, AccuracyM: 10})
	return err
}

func busyDE(tripID string) *models.DeliveryExecutive {
	return &models.DeliveryExecutive{
		PhoneNumber:     "+260971000001",
		DEID:            "DE-1",
		Status:          models.DEStatusBusy,
		AssignedStoreID: returnScanStore,
		CurrentTripID:   tripID,
	}
}

func openTrip(status models.TripStatus) *models.Trip {
	return &models.Trip{TripID: "T1", StoreID: returnScanStore, DEID: "DE-1", Status: status}
}

func TestStartDuty_BusyOutForDelivery_StampsReturnAndStaysBusy(t *testing.T) {
	f := newReturnScanFixture(busyDE("T1"), openTrip(models.TripStatusOutForDelivery))

	err := f.scanAt(storeLat, storeLng)

	if err == nil || !strings.Contains(err.Error(), "Return recorded") || !strings.Contains(err.Error(), "active delivery") {
		t.Fatalf("err = %v, want a 'Return recorded' error that still says 'active delivery'", err)
	}
	if f.trips.stamped["T1"] == "" {
		t.Fatal("returned_at not stamped on the open trip")
	}
	if f.deRepo.eligibleCalls != 0 {
		t.Fatal("busy rider must not be moved to eligible")
	}
	if len(f.events.events) != 1 || f.events.events[0].Reason != models.ReasonScanReturnWhileBusy {
		t.Fatalf("events = %+v, want one scan_return_while_busy", f.events.events)
	}
	ev := f.events.events[0]
	if ev.FromState != models.DEStatusBusy || ev.ToState != models.DEStatusBusy || ev.StoreID != returnScanStore {
		t.Fatalf("event = %+v, want busy→busy at store %s", ev, returnScanStore)
	}
}

func TestStartDuty_BusyOverCashLimit_StillRecordsReturn(t *testing.T) {
	de := busyDE("T1")
	de.InHandCashZMW = 5000
	f := newReturnScanFixture(de, openTrip(models.TripStatusOutForDelivery))

	_ = f.scanAt(storeLat, storeLng)

	if f.trips.stamped["T1"] == "" {
		t.Fatal("cash limit must not block recording the return")
	}
}

func TestStartDuty_BusyBeforePickup_RejectsWithoutStamp(t *testing.T) {
	f := newReturnScanFixture(busyDE("T1"), openTrip(models.TripStatusAccepted))

	err := f.scanAt(storeLat, storeLng)

	if err == nil || err.Error() != "cannot start duty while on an active delivery" {
		t.Fatalf("err = %v, want the unchanged busy error", err)
	}
	if len(f.trips.stamped) != 0 || len(f.events.events) != 0 {
		t.Fatal("pre-pickup scan must not stamp or log a return")
	}
}

func TestStartDuty_BusyOutsideGeofence_RejectsWithoutStamp(t *testing.T) {
	f := newReturnScanFixture(busyDE("T1"), openTrip(models.TripStatusOutForDelivery))

	err := f.scanAt(storeLat+0.05, storeLng)

	if err == nil || !strings.Contains(err.Error(), "geofence") {
		t.Fatalf("err = %v, want geofence rejection", err)
	}
	if len(f.trips.stamped) != 0 {
		t.Fatal("scan outside the geofence must not stamp a return")
	}
}

func TestStartDuty_FreeScan_StampsLastTrip(t *testing.T) {
	de := busyDE("")
	de.Status = models.DEStatusFree
	de.LastTripID = "T0"
	f := newReturnScanFixture(de)

	if err := f.scanAt(storeLat, storeLng); err != nil {
		t.Fatalf("StartDuty: %v", err)
	}
	if f.trips.stamped["T0"] == "" {
		t.Fatal("free scan must stamp returned_at on the rider's last trip")
	}
	if f.deRepo.eligibleCalls != 1 {
		t.Fatal("free scan must still move the rider to eligible")
	}
}

func TestStartDuty_OfflineScan_DoesNotStampLastTrip(t *testing.T) {
	de := busyDE("")
	de.Status = models.DEStatusOffline
	de.LastTripID = "T0"
	f := newReturnScanFixture(de)

	if err := f.scanAt(storeLat, storeLng); err != nil {
		t.Fatalf("StartDuty: %v", err)
	}
	if len(f.trips.stamped) != 0 {
		t.Fatal("scan_start after going offline is not a return")
	}
}
