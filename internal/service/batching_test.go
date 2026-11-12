package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

type fakeBatchCfg struct{ cfg *models.BatchingConfig }

func (f *fakeBatchCfg) Get(context.Context) (*models.BatchingConfig, error) {
	if f.cfg == nil {
		return &models.BatchingConfig{}, nil
	}
	return f.cfg, nil
}

type fakeBatchTrips struct {
	byID    map[string]*models.Trip
	byOrder map[string]*models.Trip
	seq     int
	persist []repository.MergePersistInput
	err     error
}

func (f *fakeBatchTrips) LiveTripByOrderID(_ context.Context, orderID string) (*models.Trip, error) {
	t := f.byOrder[orderID]
	if t == nil || !models.IsLiveTripStatus(t.Status) {
		return nil, nil
	}
	return t, nil
}

func (f *fakeBatchTrips) GetByID(_ context.Context, tripID string) (*models.Trip, error) {
	return f.byID[tripID], nil
}

func (f *fakeBatchTrips) AssignIDs(_ context.Context, trip *models.Trip) error {
	f.seq++
	if trip.TripID == "" {
		trip.TripID = fmt.Sprintf("NEW-%d", f.seq)
	}
	for i := range trip.Tasks {
		if trip.Tasks[i].TaskID == "" {
			trip.Tasks[i].TaskID = fmt.Sprintf("%s-t%d", trip.TripID, i)
		}
	}
	return nil
}

func (f *fakeBatchTrips) PersistMerge(_ context.Context, in repository.MergePersistInput) error {
	if f.err != nil {
		return f.err
	}
	f.persist = append(f.persist, in)
	if in.NewTrip != nil {
		if f.byID == nil {
			f.byID = map[string]*models.Trip{}
		}
		if f.byOrder == nil {
			f.byOrder = map[string]*models.Trip{}
		}
		f.byID[in.NewTrip.TripID] = in.NewTrip
		for _, oid := range in.NewTrip.LiveOrderIDs() {
			f.byOrder[oid] = in.NewTrip
		}
	}
	return nil
}

type fakeBatchDEs struct {
	byPhone map[string]*models.DeliveryExecutive
}

func (f *fakeBatchDEs) GetByPhone(_ context.Context, phone string) (*models.DeliveryExecutive, error) {
	return f.byPhone[phone], nil
}

func (f *fakeBatchDEs) ScanAll(context.Context) ([]*models.DeliveryExecutive, error) {
	var out []*models.DeliveryExecutive
	for _, de := range f.byPhone {
		out = append(out, de)
	}
	return out, nil
}

type fakeBatchJava struct {
	status string
	order  *JavaOrder
}

func (f *fakeBatchJava) GetOrderStatus(context.Context, string) (string, error) {
	if f.status == "" {
		return "READY_FOR_DELIVERY", nil
	}
	return f.status, nil
}

func (f *fakeBatchJava) GetOrder(_ context.Context, orderID string) (*JavaOrder, error) {
	if f.order != nil {
		return f.order, nil
	}
	return &JavaOrder{OrderID: orderID, StoreID: "221", Delivery: JavaDelivery{Lat: -15.42, Lng: 28.32, Phone: "+2601", Name: "Pat"}}, nil
}

type fakeDarkstores struct{ ds *models.Darkstore }

func (f *fakeDarkstores) GetByID(context.Context, string) (*models.Darkstore, error) {
	return f.ds, nil
}

type fakePayoutCfg struct{}

func (f *fakePayoutCfg) Get(context.Context) (*models.PayoutConfig, error) {
	return &models.PayoutConfig{RatePerKmZMW: 2, MinutesPerKm: 4}, nil
}

type fakeCashCfg struct{ limit float64 }

func (f *fakeCashCfg) Get(context.Context) (*models.CashConfig, error) {
	return &models.CashConfig{LimitZMW: f.limit}, nil
}

type fakeDist struct {
	err error
	km  float64
}

func (f *fakeDist) DistanceKM(context.Context, float64, float64, float64, float64) (float64, error) {
	if f.err != nil {
		return 0, f.err
	}
	if f.km != 0 {
		return f.km, nil
	}
	return 1.2, nil
}

type captureNotifier struct {
	reqs []models.NotificationSendRequest
}

func (c *captureNotifier) Send(_ context.Context, req models.NotificationSendRequest) models.NotificationSendResult {
	c.reqs = append(c.reqs, req)
	return models.NotificationSendResult{Status: models.SendStatusSent}
}
func (c *captureNotifier) UpsertDeviceToken(context.Context, models.RecipientType, string, string, string) error {
	return nil
}
func (c *captureNotifier) ClearDeviceToken(context.Context, models.RecipientType, string) error {
	return nil
}

func testStore() *models.Darkstore {
	return &models.Darkstore{DarkstoreID: "221", Name: "Manda Hill", Latitude: -15.4, Longitude: 28.3, IsActive: true}
}

func acceptedSingleton(tripID, orderID, deID, phone string, dropLat, dropLng float64) *models.Trip {
	return &models.Trip{
		TripID:    tripID,
		OrderID:   orderID,
		OrderIDs:  []string{orderID},
		StoreID:   "221",
		DEID:      deID,
		DEPhone:   phone,
		Status:    models.TripStatusAccepted,
		UpdatedAt: "2026-11-10T10:00:00Z",
		CreatedAt: "2026-11-10T09:00:00Z",
		Tasks: []models.Task{
			{TaskID: tripID + "-p", Type: models.TaskTypePickup, Status: models.TaskStatusCreated, OrderID: orderID, Lat: -15.4, Lng: 28.3},
			{TaskID: tripID + "-d", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OrderID: orderID, Lat: dropLat, Lng: dropLng, OTP: "1111", Payment: &models.Payment{CollectCash: true, AmountZMW: 50}},
		},
	}
}

func testBatching(trips *fakeBatchTrips, des *fakeBatchDEs, dist *fakeDist, ntf *captureNotifier) *BatchingService {
	return &BatchingService{
		cfg:        &fakeBatchCfg{cfg: &models.BatchingConfig{Enabled: true, MinDriverAppVersion: "1.4.0"}},
		trips:      trips,
		des:        des,
		java:       &fakeBatchJava{status: "READY_FOR_DELIVERY"},
		darkstores: &fakeDarkstores{ds: testStore()},
		payoutCfg:  &fakePayoutCfg{},
		cashCfg:    &fakeCashCfg{limit: 500},
		distance:   dist,
		notifier:   ntf,
		logger:     logrus.New(),
	}
}

func TestBatching_FlagOff(t *testing.T) {
	s := testBatching(&fakeBatchTrips{}, &fakeBatchDEs{}, &fakeDist{}, &captureNotifier{})
	s.cfg = &fakeBatchCfg{cfg: &models.BatchingConfig{Enabled: false}}
	_, err := s.Candidates(context.Background(), "ORD-B")
	if !errors.Is(err, ErrBatchingDisabled) {
		t.Fatalf("got %v, want ErrBatchingDisabled", err)
	}
	_, err = s.Confirm(context.Background(), BatchConfirmInput{OrderID: "ORD-B", TargetDEPhone: "+2601"})
	if !errors.Is(err, ErrBatchingDisabled) {
		t.Fatalf("confirm: got %v", err)
	}
}

func TestBatching_AppTooOld(t *testing.T) {
	target := acceptedSingleton("T-A", "ORD-A", "de-a", "+260A", -15.41, 28.31)
	source := acceptedSingleton("T-B", "ORD-B", "de-b", "+260B", -15.42, 28.32)
	trips := &fakeBatchTrips{
		byID:    map[string]*models.Trip{"T-A": target, "T-B": source},
		byOrder: map[string]*models.Trip{"ORD-A": target, "ORD-B": source},
	}
	des := &fakeBatchDEs{byPhone: map[string]*models.DeliveryExecutive{
		"+260A": {DEID: "de-a", PhoneNumber: "+260A", Status: models.DEStatusBusy, CurrentTripID: "T-A", CurrentStoreID: "221", LastAppVersion: "1.0.0"},
		"+260B": {DEID: "de-b", PhoneNumber: "+260B", Status: models.DEStatusBusy, CurrentTripID: "T-B", CurrentStoreID: "221", LastAppVersion: "1.5.0"},
	}}
	s := testBatching(trips, des, &fakeDist{}, &captureNotifier{})
	_, err := s.Confirm(context.Background(), BatchConfirmInput{
		OrderID: "ORD-B", TargetDEPhone: "+260A",
		ExpectedUpdatedAt: map[string]string{"T-A": target.UpdatedAt, "T-B": source.UpdatedAt},
	})
	if !errors.Is(err, ErrAppTooOld) {
		t.Fatalf("got %v, want ErrAppTooOld", err)
	}
}

func TestBatching_MergeTwoAccepted(t *testing.T) {
	target := acceptedSingleton("T-A", "ORD-A", "de-a", "+260A", -15.41, 28.31)
	source := acceptedSingleton("T-B", "ORD-B", "de-b", "+260B", -15.42, 28.32)
	trips := &fakeBatchTrips{
		byID:    map[string]*models.Trip{"T-A": target, "T-B": source},
		byOrder: map[string]*models.Trip{"ORD-A": target, "ORD-B": source},
	}
	des := &fakeBatchDEs{byPhone: map[string]*models.DeliveryExecutive{
		"+260A": {DEID: "de-a", PhoneNumber: "+260A", Name: "Ada", Status: models.DEStatusBusy, CurrentTripID: "T-A", CurrentStoreID: "221", LastAppVersion: "1.5.0"},
		"+260B": {DEID: "de-b", PhoneNumber: "+260B", Name: "Bo", Status: models.DEStatusBusy, CurrentTripID: "T-B", CurrentStoreID: "221", LastAppVersion: "1.5.0"},
	}}
	ntf := &captureNotifier{}
	s := testBatching(trips, des, &fakeDist{}, ntf)
	got, err := s.Confirm(context.Background(), BatchConfirmInput{
		OrderID: "ORD-B", TargetDEPhone: "+260A",
		ExpectedUpdatedAt: map[string]string{"T-A": target.UpdatedAt, "T-B": source.UpdatedAt},
	})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if got.Status != string(models.TripStatusAccepted) {
		t.Fatalf("status = %s, want accepted", got.Status)
	}
	if len(got.OrderIDs) != 2 || got.OrderIDs[0] != "ORD-A" || got.OrderIDs[1] != "ORD-B" {
		t.Fatalf("order_ids = %v", got.OrderIDs)
	}
	if len(trips.persist) != 1 {
		t.Fatalf("persist calls = %d", len(trips.persist))
	}
	p := trips.persist[0]
	var pickups, drops int
	for _, task := range p.NewTrip.Tasks {
		switch task.Type {
		case models.TaskTypePickup:
			pickups++
			if task.Status != models.TaskStatusCreated {
				t.Fatalf("pickup %s status %s", task.OrderID, task.Status)
			}
		case models.TaskTypeDrop:
			drops++
		}
	}
	if pickups != 2 || drops != 2 {
		t.Fatalf("tasks pickups=%d drops=%d", pickups, drops)
	}
	if len(p.CloseTrips) != 2 {
		t.Fatalf("close trips = %d", len(p.CloseTrips))
	}
	if p.FreeLoser != true {
		t.Fatal("stealing a singleton should free the loser")
	}
	if p.Leftover != nil {
		t.Fatal("no leftover expected when stealing last order")
	}
	if got.DistanceMethod != models.DistanceMethodGoogle {
		t.Fatalf("distance_method = %s", got.DistanceMethod)
	}
	var updated int
	for _, req := range ntf.reqs {
		if req.EventType == "TRIP_UPDATED" {
			updated++
		}
		if req.EventType == "ORDER_ASSIGNED" {
			t.Fatal("must not send accept/assigned push on merge")
		}
	}
	if updated < 1 {
		t.Fatalf("expected TRIP_UPDATED, got %+v", ntf.reqs)
	}
}

func TestBatching_MergeOntoOFD_ClearsReached(t *testing.T) {
	target := acceptedSingleton("T-A", "ORD-A", "de-a", "+260A", -15.41, 28.31)
	target.Status = models.TripStatusOutForDelivery
	target.Tasks[0].Status = models.TaskStatusCompleted
	target.Tasks[1].Status = models.TaskStatusReached
	target.Tasks[1].ReachedAt = "2026-11-10T10:05:00Z"
	source := acceptedSingleton("T-B", "ORD-B", "de-b", "+260B", -15.42, 28.32)
	trips := &fakeBatchTrips{
		byID:    map[string]*models.Trip{"T-A": target, "T-B": source},
		byOrder: map[string]*models.Trip{"ORD-A": target, "ORD-B": source},
	}
	des := &fakeBatchDEs{byPhone: map[string]*models.DeliveryExecutive{
		"+260A": {DEID: "de-a", PhoneNumber: "+260A", Status: models.DEStatusBusy, CurrentTripID: "T-A", CurrentStoreID: "221", LastAppVersion: "1.5.0"},
		"+260B": {DEID: "de-b", PhoneNumber: "+260B", Status: models.DEStatusBusy, CurrentTripID: "T-B", CurrentStoreID: "221", LastAppVersion: "1.5.0"},
	}}
	s := testBatching(trips, des, &fakeDist{}, &captureNotifier{})
	got, err := s.Confirm(context.Background(), BatchConfirmInput{
		OrderID: "ORD-B", TargetDEPhone: "+260A",
		ExpectedUpdatedAt: map[string]string{"T-A": target.UpdatedAt, "T-B": source.UpdatedAt},
	})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if got.Status != string(models.TripStatusAccepted) {
		t.Fatalf("status = %s, want accepted (incoming pickup still open)", got.Status)
	}
	newTrip := trips.persist[0].NewTrip
	pickupA := newTrip.PickupForOrder("ORD-A")
	if pickupA == nil || pickupA.Status != models.TaskStatusCompleted {
		t.Fatalf("pickup A = %+v", pickupA)
	}
	dropA := newTrip.DropForOrder("ORD-A")
	if dropA == nil || dropA.Status != models.TaskStatusCreated || dropA.ReachedAt != "" {
		t.Fatalf("drop A reached not cleared: %+v", dropA)
	}
}

func TestBatching_BothOFD_PinsTargetDropFirst(t *testing.T) {
	target := acceptedSingleton("T-A", "ORD-A", "de-a", "+260A", -15.41, 28.31)
	target.Status = models.TripStatusOutForDelivery
	target.Tasks[0].Status = models.TaskStatusCompleted
	source := acceptedSingleton("T-B", "ORD-B", "de-b", "+260B", -15.50, 28.50)
	source.Status = models.TripStatusOutForDelivery
	source.Tasks[0].Status = models.TaskStatusCompleted
	trips := &fakeBatchTrips{
		byID:    map[string]*models.Trip{"T-A": target, "T-B": source},
		byOrder: map[string]*models.Trip{"ORD-A": target, "ORD-B": source},
	}
	des := &fakeBatchDEs{byPhone: map[string]*models.DeliveryExecutive{
		"+260A": {DEID: "de-a", PhoneNumber: "+260A", Status: models.DEStatusBusy, CurrentTripID: "T-A", CurrentStoreID: "221", LastAppVersion: "1.5.0"},
		"+260B": {DEID: "de-b", PhoneNumber: "+260B", Status: models.DEStatusBusy, CurrentTripID: "T-B", CurrentStoreID: "221", LastAppVersion: "1.5.0"},
	}}
	s := testBatching(trips, des, &fakeDist{}, &captureNotifier{})
	got, err := s.Confirm(context.Background(), BatchConfirmInput{
		OrderID: "ORD-B", TargetDEPhone: "+260A",
		ExpectedUpdatedAt: map[string]string{"T-A": target.UpdatedAt, "T-B": source.UpdatedAt},
	})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if got.Status != string(models.TripStatusOutForDelivery) {
		t.Fatalf("status = %s, want out_for_delivery", got.Status)
	}
	drops := trips.persist[0].NewTrip.OpenDrops()
	if len(drops) < 1 || drops[0].OrderID != "ORD-A" {
		t.Fatalf("first open drop = %+v, want ORD-A pinned", drops)
	}
}

func TestBatching_StealLeavesLeftover(t *testing.T) {
	target := acceptedSingleton("T-A", "ORD-A", "de-a", "+260A", -15.41, 28.31)
	loser := &models.Trip{
		TripID: "T-L", OrderID: "ORD-X", OrderIDs: []string{"ORD-X", "ORD-B"}, StoreID: "221",
		DEID: "de-l", DEPhone: "+260L", Status: models.TripStatusAccepted, UpdatedAt: "2026-11-10T10:00:00Z",
		Tasks: []models.Task{
			{TaskID: "px", Type: models.TaskTypePickup, Status: models.TaskStatusCreated, OrderID: "ORD-X", Lat: -15.4, Lng: 28.3},
			{TaskID: "pb", Type: models.TaskTypePickup, Status: models.TaskStatusCreated, OrderID: "ORD-B", Lat: -15.4, Lng: 28.3},
			{TaskID: "dx", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OrderID: "ORD-X", Lat: -15.43, Lng: 28.33, OTP: "2222"},
			{TaskID: "db", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OrderID: "ORD-B", Lat: -15.44, Lng: 28.34, OTP: "3333"},
		},
	}
	trips := &fakeBatchTrips{
		byID:    map[string]*models.Trip{"T-A": target, "T-L": loser},
		byOrder: map[string]*models.Trip{"ORD-A": target, "ORD-B": loser, "ORD-X": loser},
	}
	des := &fakeBatchDEs{byPhone: map[string]*models.DeliveryExecutive{
		"+260A": {DEID: "de-a", PhoneNumber: "+260A", Status: models.DEStatusBusy, CurrentTripID: "T-A", CurrentStoreID: "221", LastAppVersion: "1.5.0"},
		"+260L": {DEID: "de-l", PhoneNumber: "+260L", Status: models.DEStatusBusy, CurrentTripID: "T-L", CurrentStoreID: "221", LastAppVersion: "1.5.0"},
	}}
	ntf := &captureNotifier{}
	s := testBatching(trips, des, &fakeDist{}, ntf)
	_, err := s.Confirm(context.Background(), BatchConfirmInput{
		OrderID: "ORD-B", TargetDEPhone: "+260A",
		ExpectedUpdatedAt: map[string]string{"T-A": target.UpdatedAt, "T-L": loser.UpdatedAt},
	})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	p := trips.persist[0]
	if p.Leftover == nil {
		t.Fatal("expected leftover for loser")
	}
	if p.FreeLoser {
		t.Fatal("loser still has ORD-X")
	}
	if p.Leftover.PickupForOrder("ORD-X") == nil || p.Leftover.PickupForOrder("ORD-B") != nil {
		t.Fatalf("leftover orders wrong: %+v", p.Leftover.LiveOrderIDs())
	}
	var updated int
	for _, req := range ntf.reqs {
		if req.EventType == "TRIP_UPDATED" {
			updated++
		}
	}
	if updated < 2 {
		t.Fatalf("both DEs should get TRIP_UPDATED, got %d", updated)
	}
}

func TestBatching_NoSourceSynthesizesTasks(t *testing.T) {
	target := acceptedSingleton("T-A", "ORD-A", "de-a", "+260A", -15.41, 28.31)
	trips := &fakeBatchTrips{
		byID:    map[string]*models.Trip{"T-A": target},
		byOrder: map[string]*models.Trip{"ORD-A": target},
	}
	des := &fakeBatchDEs{byPhone: map[string]*models.DeliveryExecutive{
		"+260A": {DEID: "de-a", PhoneNumber: "+260A", Status: models.DEStatusBusy, CurrentTripID: "T-A", CurrentStoreID: "221", LastAppVersion: "1.5.0"},
	}}
	s := testBatching(trips, des, &fakeDist{}, &captureNotifier{})
	s.java = &fakeBatchJava{status: "READY_FOR_DELIVERY", order: &JavaOrder{
		OrderID: "ORD-B", StoreID: "221", Delivery: JavaDelivery{Lat: -15.45, Lng: 28.35, Phone: "+2609", Name: "Pat"},
	}}
	got, err := s.Confirm(context.Background(), BatchConfirmInput{
		OrderID: "ORD-B", TargetDEPhone: "+260A",
		ExpectedUpdatedAt: map[string]string{"T-A": target.UpdatedAt},
	})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if len(got.OrderIDs) != 2 {
		t.Fatalf("order_ids = %v", got.OrderIDs)
	}
	if trips.persist[0].NewTrip.PickupForOrder("ORD-B") == nil {
		t.Fatal("synthesized pickup B missing")
	}
}

func TestBatching_GoogleNoRoute_Haversine(t *testing.T) {
	target := acceptedSingleton("T-A", "ORD-A", "de-a", "+260A", -15.41, 28.31)
	source := acceptedSingleton("T-B", "ORD-B", "de-b", "+260B", -15.42, 28.32)
	trips := &fakeBatchTrips{
		byID:    map[string]*models.Trip{"T-A": target, "T-B": source},
		byOrder: map[string]*models.Trip{"ORD-A": target, "ORD-B": source},
	}
	des := &fakeBatchDEs{byPhone: map[string]*models.DeliveryExecutive{
		"+260A": {DEID: "de-a", PhoneNumber: "+260A", Status: models.DEStatusBusy, CurrentTripID: "T-A", CurrentStoreID: "221", LastAppVersion: "1.5.0"},
		"+260B": {DEID: "de-b", PhoneNumber: "+260B", Status: models.DEStatusBusy, CurrentTripID: "T-B", CurrentStoreID: "221", LastAppVersion: "1.5.0"},
	}}
	s := testBatching(trips, des, &fakeDist{err: ErrNoRoute}, &captureNotifier{})
	got, err := s.Confirm(context.Background(), BatchConfirmInput{
		OrderID: "ORD-B", TargetDEPhone: "+260A",
		ExpectedUpdatedAt: map[string]string{"T-A": target.UpdatedAt, "T-B": source.UpdatedAt},
	})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if got.DistanceMethod != models.DistanceMethodHaversine {
		t.Fatalf("distance_method = %s", got.DistanceMethod)
	}
	hasFallback := false
	for _, w := range got.Warnings {
		if w == "haversine_fallback" {
			hasFallback = true
		}
		if w == "distance_failed" {
			t.Fatal("must not warn distance_failed")
		}
	}
	if !hasFallback {
		t.Fatalf("warnings = %v, want haversine_fallback", got.Warnings)
	}
}

func TestBatching_StaleMerge(t *testing.T) {
	target := acceptedSingleton("T-A", "ORD-A", "de-a", "+260A", -15.41, 28.31)
	source := acceptedSingleton("T-B", "ORD-B", "de-b", "+260B", -15.42, 28.32)
	trips := &fakeBatchTrips{
		byID:    map[string]*models.Trip{"T-A": target, "T-B": source},
		byOrder: map[string]*models.Trip{"ORD-A": target, "ORD-B": source},
	}
	des := &fakeBatchDEs{byPhone: map[string]*models.DeliveryExecutive{
		"+260A": {DEID: "de-a", PhoneNumber: "+260A", Status: models.DEStatusBusy, CurrentTripID: "T-A", CurrentStoreID: "221", LastAppVersion: "1.5.0"},
		"+260B": {DEID: "de-b", PhoneNumber: "+260B", Status: models.DEStatusBusy, CurrentTripID: "T-B", CurrentStoreID: "221", LastAppVersion: "1.5.0"},
	}}
	s := testBatching(trips, des, &fakeDist{}, &captureNotifier{})
	_, err := s.Confirm(context.Background(), BatchConfirmInput{
		OrderID: "ORD-B", TargetDEPhone: "+260A",
		ExpectedUpdatedAt: map[string]string{"T-A": "old", "T-B": source.UpdatedAt},
	})
	if !errors.Is(err, ErrStaleMerge) {
		t.Fatalf("got %v, want ErrStaleMerge", err)
	}
	if len(trips.persist) != 0 {
		t.Fatal("must not persist on stale merge")
	}
}

func TestBatching_CashExceedsStillSucceeds(t *testing.T) {
	target := acceptedSingleton("T-A", "ORD-A", "de-a", "+260A", -15.41, 28.31)
	source := acceptedSingleton("T-B", "ORD-B", "de-b", "+260B", -15.42, 28.32)
	trips := &fakeBatchTrips{
		byID:    map[string]*models.Trip{"T-A": target, "T-B": source},
		byOrder: map[string]*models.Trip{"ORD-A": target, "ORD-B": source},
	}
	des := &fakeBatchDEs{byPhone: map[string]*models.DeliveryExecutive{
		"+260A": {DEID: "de-a", PhoneNumber: "+260A", Status: models.DEStatusBusy, CurrentTripID: "T-A", CurrentStoreID: "221", LastAppVersion: "1.5.0", InHandCashZMW: 480},
		"+260B": {DEID: "de-b", PhoneNumber: "+260B", Status: models.DEStatusBusy, CurrentTripID: "T-B", CurrentStoreID: "221", LastAppVersion: "1.5.0"},
	}}
	s := testBatching(trips, des, &fakeDist{}, &captureNotifier{})
	got, err := s.Confirm(context.Background(), BatchConfirmInput{
		OrderID: "ORD-B", TargetDEPhone: "+260A",
		ExpectedUpdatedAt: map[string]string{"T-A": target.UpdatedAt, "T-B": source.UpdatedAt},
	})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	found := false
	for _, w := range got.Warnings {
		if w == "cash_exceeds" {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v, want cash_exceeds", got.Warnings)
	}
}

func TestBatching_AlreadyOnRider(t *testing.T) {
	target := acceptedSingleton("T-A", "ORD-A", "de-a", "+260A", -15.41, 28.31)
	target.OrderIDs = []string{"ORD-A", "ORD-B"}
	target.Tasks = append(target.Tasks,
		models.Task{TaskID: "pb", Type: models.TaskTypePickup, Status: models.TaskStatusCreated, OrderID: "ORD-B"},
		models.Task{TaskID: "db", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OrderID: "ORD-B"},
	)
	trips := &fakeBatchTrips{
		byID:    map[string]*models.Trip{"T-A": target},
		byOrder: map[string]*models.Trip{"ORD-A": target, "ORD-B": target},
	}
	des := &fakeBatchDEs{byPhone: map[string]*models.DeliveryExecutive{
		"+260A": {DEID: "de-a", PhoneNumber: "+260A", Status: models.DEStatusBusy, CurrentTripID: "T-A", CurrentStoreID: "221", LastAppVersion: "1.5.0"},
	}}
	s := testBatching(trips, des, &fakeDist{}, &captureNotifier{})
	_, err := s.Confirm(context.Background(), BatchConfirmInput{
		OrderID: "ORD-B", TargetDEPhone: "+260A",
		ExpectedUpdatedAt: map[string]string{"T-A": target.UpdatedAt},
	})
	if !errors.Is(err, ErrAlreadyOnRider) {
		t.Fatalf("got %v", err)
	}
}
