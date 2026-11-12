package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

var (
	ErrBatchingDisabled  = errors.New("batching disabled")
	ErrAppTooOld         = errors.New("driver app too old")
	ErrStaleMerge        = errors.New("stale merge")
	ErrTripInactive      = errors.New("trip inactive")
	ErrAlreadyOnRider    = errors.New("order already on rider")
	ErrWrongStore        = errors.New("wrong store")
	ErrNoLiveTrip        = errors.New("no live trip")
	ErrDriverNotBusy     = errors.New("driver not busy")
	ErrOrderNotBatchable = errors.New("order not batchable")
)

type batchingCfgStore interface {
	Get(ctx context.Context) (*models.BatchingConfig, error)
}

type batchingTripStore interface {
	LiveTripByOrderID(ctx context.Context, orderID string) (*models.Trip, error)
	GetByID(ctx context.Context, tripID string) (*models.Trip, error)
	AssignIDs(ctx context.Context, trip *models.Trip) error
	PersistMerge(ctx context.Context, in repository.MergePersistInput) error
}

type batchingDEStore interface {
	GetByPhone(ctx context.Context, phone string) (*models.DeliveryExecutive, error)
	ScanAll(ctx context.Context) ([]*models.DeliveryExecutive, error)
}

type batchingJava interface {
	GetOrderStatus(ctx context.Context, orderID string) (string, error)
	GetOrder(ctx context.Context, orderID string) (*JavaOrder, error)
}

type batchingDarkstore interface {
	GetByID(ctx context.Context, id string) (*models.Darkstore, error)
}

type batchingPayoutCfg interface {
	Get(ctx context.Context) (*models.PayoutConfig, error)
}

type batchingCashCfg interface {
	Get(ctx context.Context) (*models.CashConfig, error)
}

type BatchingService struct {
	cfg        batchingCfgStore
	trips      batchingTripStore
	des        batchingDEStore
	java       batchingJava
	darkstores batchingDarkstore
	payoutCfg  batchingPayoutCfg
	cashCfg    batchingCashCfg
	distance   distancer
	fareEngine *FareEngine
	payout     *PayoutService
	notifier   NotificationService
	logger     *logrus.Logger
}

func NewBatchingService(
	cfg *repository.BatchingConfigRepository,
	trips *repository.TripRepository,
	des *repository.DERepository,
	java *JavaOrderClient,
	darkstores *repository.DarkstoreRepository,
	payoutCfg *repository.PayoutConfigRepository,
	cashCfg *repository.CashConfigRepository,
	distance *DistanceService,
	fareEngine *FareEngine,
	payout *PayoutService,
	notifier NotificationService,
	logger *logrus.Logger,
) *BatchingService {
	return &BatchingService{
		cfg: cfg, trips: trips, des: des, java: java, darkstores: darkstores,
		payoutCfg: payoutCfg, cashCfg: cashCfg, distance: distance,
		fareEngine: fareEngine, payout: payout, notifier: notifier, logger: logger,
	}
}

func (s *BatchingService) loadFlag(ctx context.Context) (*models.BatchingConfig, error) {
	if s.cfg == nil {
		return &models.BatchingConfig{}, nil
	}
	return s.cfg.Get(ctx)
}

func (s *BatchingService) requireEnabled(ctx context.Context) (*models.BatchingConfig, error) {
	cfg, err := s.loadFlag(ctx)
	if err != nil {
		return nil, err
	}
	if cfg == nil || !cfg.Enabled {
		return nil, ErrBatchingDisabled
	}
	return cfg, nil
}

type BatchCandidate struct {
	Phone        string `json:"phone"`
	Name         string `json:"name"`
	TripID       string `json:"trip_id"`
	TripUpdatedAt string `json:"trip_updated_at"`
	AppOK        bool   `json:"app_ok"`
	CashExceeds  bool   `json:"cash_exceeds"`
}

type BatchCandidatesResult struct {
	SourceTripID        string           `json:"source_trip_id,omitempty"`
	SourceTripUpdatedAt string           `json:"source_trip_updated_at,omitempty"`
	SourceRiderName     string           `json:"source_rider_name,omitempty"`
	Candidates          []BatchCandidate `json:"candidates"`
}

func (s *BatchingService) Candidates(ctx context.Context, orderID string) (*BatchCandidatesResult, error) {
	op := logging.Start(ctx, s.logger, "BatchingService.Candidates", logrus.Fields{"order_id": orderID})
	defer op.End()

	cfg, err := s.requireEnabled(ctx)
	if err != nil {
		return nil, err
	}

	source, err := s.trips.LiveTripByOrderID(ctx, orderID)
	if err != nil {
		return nil, op.Fail(err)
	}

	storeID, err := s.orderStoreID(ctx, orderID, source)
	if err != nil {
		return nil, op.Fail(err)
	}

	out := &BatchCandidatesResult{Candidates: []BatchCandidate{}}
	excludePhone := ""
	if source != nil {
		out.SourceTripID = source.TripID
		out.SourceTripUpdatedAt = source.UpdatedAt
		excludePhone = source.DEPhone
		if source.DEPhone != "" {
			if de, derr := s.des.GetByPhone(ctx, source.DEPhone); derr == nil && de != nil {
				out.SourceRiderName = de.Name
			}
		}
	}

	cashLimit := (&models.CashConfig{}).EffectiveLimitZMW()
	if s.cashCfg != nil {
		if cc, cErr := s.cashCfg.Get(ctx); cErr != nil {
			s.logger.WithError(cErr).Warn("batching: cash config lookup failed; using default")
		} else {
			cashLimit = cc.EffectiveLimitZMW()
		}
	}

	des, err := s.des.ScanAll(ctx)
	if err != nil {
		return nil, op.Fail(err)
	}

	incomingCOD := 0.0
	if source != nil {
		if d := source.DropForOrder(orderID); d != nil && d.Payment != nil && d.Payment.CollectCash && d.Status != models.TaskStatusCompleted {
			incomingCOD = d.Payment.AmountZMW
		} else if source.Payment != nil && source.Payment.CollectCash {
			incomingCOD = source.Payment.AmountZMW
		}
	} else if s.java != nil {
		if o, oerr := s.java.GetOrder(ctx, orderID); oerr == nil && o != nil {
			p := paymentFromOrder(*o)
			if p.CollectCash {
				incomingCOD = p.AmountZMW
			}
		}
	}

	for _, de := range des {
		if de == nil || de.Status != models.DEStatusBusy || de.CurrentTripID == "" {
			continue
		}
		if de.CurrentStoreID != storeID {
			continue
		}
		if excludePhone != "" && de.PhoneNumber == excludePhone {
			continue
		}
		trip, terr := s.trips.GetByID(ctx, de.CurrentTripID)
		if terr != nil || trip == nil || !models.IsLiveTripStatus(trip.Status) {
			continue
		}
		if trip.StoreID != storeID {
			continue
		}
		remaining := remainingCOD(trip.Tasks) + incomingCOD
		out.Candidates = append(out.Candidates, BatchCandidate{
			Phone:         de.PhoneNumber,
			Name:          de.Name,
			TripID:        trip.TripID,
			TripUpdatedAt: trip.UpdatedAt,
			AppOK:         cfg.AppVersionOK(de.LastAppVersion),
			CashExceeds:   de.InHandCashZMW+remaining > cashLimit,
		})
	}
	return out, nil
}

type BatchConfirmInput struct {
	OrderID           string
	TargetDEPhone     string
	ExpectedUpdatedAt map[string]string
}

type BatchConfirmResult struct {
	TripID         string   `json:"trip_id"`
	OrderIDs       []string `json:"order_ids"`
	Status         string   `json:"status"`
	DistanceMethod string   `json:"distance_method"`
	Warnings       []string `json:"warnings"`
}

func (s *BatchingService) Confirm(ctx context.Context, in BatchConfirmInput) (*BatchConfirmResult, error) {
	op := logging.Start(ctx, s.logger, "BatchingService.Confirm", logrus.Fields{
		"order_id": in.OrderID, "target": in.TargetDEPhone,
	})
	defer op.End()

	cfg, err := s.requireEnabled(ctx)
	if err != nil {
		return nil, err
	}

	sourceTrip, err := s.trips.LiveTripByOrderID(ctx, in.OrderID)
	if err != nil {
		return nil, op.Fail(err)
	}

	targetDE, err := s.des.GetByPhone(ctx, in.TargetDEPhone)
	if err != nil {
		return nil, op.Fail(err)
	}
	if targetDE == nil {
		return nil, fmt.Errorf("%w: %s", ErrDENotFound, in.TargetDEPhone)
	}
	if targetDE.Status != models.DEStatusBusy {
		return nil, ErrDriverNotBusy
	}
	if targetDE.CurrentTripID == "" {
		return nil, ErrNoLiveTrip
	}

	targetTrip, err := s.trips.GetByID(ctx, targetDE.CurrentTripID)
	if err != nil {
		return nil, op.Fail(err)
	}
	if targetTrip == nil || !models.IsLiveTripStatus(targetTrip.Status) {
		return nil, ErrNoLiveTrip
	}

	if sourceTrip != nil && sourceTrip.TripID == targetTrip.TripID {
		return nil, ErrAlreadyOnRider
	}

	var loserTrip *models.Trip
	if sourceTrip != nil && sourceTrip.DEID != targetDE.DEID {
		loserTrip = sourceTrip
	}

	if !cfg.AppVersionOK(targetDE.LastAppVersion) {
		return nil, ErrAppTooOld
	}
	if loserTrip != nil && loserTrip.DEPhone != "" {
		loserDE, lerr := s.des.GetByPhone(ctx, loserTrip.DEPhone)
		if lerr != nil {
			return nil, op.Fail(lerr)
		}
		if loserDE == nil || !cfg.AppVersionOK(loserDE.LastAppVersion) {
			return nil, ErrAppTooOld
		}
	}

	storeID, err := s.orderStoreID(ctx, in.OrderID, sourceTrip)
	if err != nil {
		return nil, op.Fail(err)
	}
	if targetTrip.StoreID != storeID {
		return nil, ErrWrongStore
	}

	if s.java != nil {
		st, jerr := s.java.GetOrderStatus(ctx, in.OrderID)
		if jerr != nil {
			return nil, op.Fail(jerr)
		}
		switch strings.ToUpper(strings.TrimSpace(st)) {
		case "CANCELLED", "DELIVERED":
			return nil, ErrOrderNotBatchable
		}
	}

	if err := checkExpectedUpdatedAt(in.ExpectedUpdatedAt, targetTrip, sourceTrip); err != nil {
		return nil, err
	}

	ds, err := s.darkstores.GetByID(ctx, storeID)
	if err != nil {
		return nil, op.Fail(err)
	}
	if ds == nil {
		return nil, fmt.Errorf("darkstore %s not found", storeID)
	}

	built, err := s.buildSuccessor(ctx, targetTrip, sourceTrip, in.OrderID, ds, targetDE)
	if err != nil {
		return nil, op.Fail(err)
	}

	var leftover *builtTrip
	freeLoser := false
	if loserTrip != nil {
		rest := tasksExcludingOrder(loserTrip, in.OrderID)
		if len(dropOrPickupOrders(rest)) == 0 {
			freeLoser = true
		} else {
			lb, berr := s.buildFromTasks(ctx, loserTrip, rest, ds, []string{loserTrip.TripID})
			if berr != nil {
				return nil, op.Fail(berr)
			}
			leftover = lb
			if leftover != nil && leftover.trip.Status == models.TripStatusCompleted {
				freeLoser = true
			}
		}
	}

	if err := s.trips.AssignIDs(ctx, built.trip); err != nil {
		return nil, op.Fail(err)
	}
	if leftover != nil {
		if err := s.trips.AssignIDs(ctx, leftover.trip); err != nil {
			return nil, op.Fail(err)
		}
	}

	closeTrips := []repository.MergeCloseTrip{{
		TripID:            targetTrip.TripID,
		ExpectedUpdatedAt: expectedAt(in.ExpectedUpdatedAt, targetTrip.TripID, targetTrip.UpdatedAt),
		OrderIDs:          targetTrip.LiveOrderIDs(),
		SupersededBy:      built.trip.TripID,
	}}
	if loserTrip != nil {
		succ := built.trip.TripID
		if leftover != nil {
			succ = leftover.trip.TripID
		}
		closeTrips = append(closeTrips, repository.MergeCloseTrip{
			TripID:            loserTrip.TripID,
			ExpectedUpdatedAt: expectedAt(in.ExpectedUpdatedAt, loserTrip.TripID, loserTrip.UpdatedAt),
			OrderIDs:          loserTrip.LiveOrderIDs(),
			SupersededBy:      succ,
		})
	}

	persist := repository.MergePersistInput{
		NewTrip:            built.trip,
		CloseTrips:         closeTrips,
		TargetPhone:        targetDE.PhoneNumber,
		TargetExpectedTrip: targetTrip.TripID,
		TargetStoreID:      targetTrip.StoreID,
		FreeTarget:         built.trip.Status == models.TripStatusCompleted,
	}
	if leftover != nil {
		persist.Leftover = leftover.trip
	}
	if loserTrip != nil {
		persist.LoserPhone = loserTrip.DEPhone
		persist.LoserExpectedTrip = loserTrip.TripID
		persist.LoserStoreID = loserTrip.StoreID
		persist.FreeLoser = freeLoser || leftover == nil
	}

	if err := s.trips.PersistMerge(ctx, persist); err != nil {
		if errors.Is(err, repository.ErrMergeConflict) {
			return nil, ErrStaleMerge
		}
		return nil, op.Fail(err)
	}

	s.sendTripUpdated(ctx, targetDE.DEID, built.trip.TripID, targetTrip.TripID)
	if leftover != nil && loserTrip != nil && leftover.trip.Status != models.TripStatusCompleted {
		s.sendTripUpdated(ctx, loserTrip.DEID, leftover.trip.TripID, loserTrip.TripID)
	}

	if built.trip.Status == models.TripStatusCompleted {
		s.payIfNeeded(built.trip, targetDE)
	}
	if leftover != nil && leftover.trip.Status == models.TripStatusCompleted && loserTrip != nil {
		if de, derr := s.des.GetByPhone(ctx, loserTrip.DEPhone); derr == nil && de != nil {
			s.payIfNeeded(leftover.trip, de)
		}
	}

	warnings := []string{}
	if built.haversine {
		warnings = append(warnings, "haversine_fallback")
	}
	cashLimit := (&models.CashConfig{}).EffectiveLimitZMW()
	if s.cashCfg != nil {
		if cc, cErr := s.cashCfg.Get(ctx); cErr == nil && cc != nil {
			cashLimit = cc.EffectiveLimitZMW()
		}
	}
	if targetDE.InHandCashZMW+remainingCOD(built.trip.Tasks) > cashLimit {
		warnings = append(warnings, "cash_exceeds")
	}

	return &BatchConfirmResult{
		TripID:         built.trip.TripID,
		OrderIDs:       built.trip.LiveOrderIDs(),
		Status:         string(built.trip.Status),
		DistanceMethod: built.trip.DistanceMethod,
		Warnings:       warnings,
	}, nil
}

// CancelOne removes one order from a multi-order live trip and rebuilds a leftover successor.
func (s *BatchingService) CancelOne(ctx context.Context, live *models.Trip, orderID string) error {
	op := logging.Start(ctx, s.logger, "BatchingService.CancelOne", logrus.Fields{
		"trip_id": live.TripID, "order_id": orderID,
	})
	defer op.End()

	ds, err := s.darkstores.GetByID(ctx, live.StoreID)
	if err != nil {
		return op.Fail(err)
	}
	if ds == nil {
		return op.Fail(fmt.Errorf("darkstore %s not found", live.StoreID))
	}

	rest := tasksExcludingOrder(live, orderID)
	if len(dropOrPickupOrders(rest)) == 0 {
		return op.Fail(fmt.Errorf("cancel-one produced empty leftover"))
	}

	built, err := s.buildFromTasks(ctx, live, rest, ds, []string{live.TripID})
	if err != nil {
		return op.Fail(err)
	}
	if err := s.trips.AssignIDs(ctx, built.trip); err != nil {
		return op.Fail(err)
	}

	free := built.trip.Status == models.TripStatusCompleted
	persist := repository.MergePersistInput{
		NewTrip:            built.trip,
		CloseTrips:         []repository.MergeCloseTrip{{TripID: live.TripID, ExpectedUpdatedAt: live.UpdatedAt, OrderIDs: live.LiveOrderIDs(), SupersededBy: built.trip.TripID}},
		TargetPhone:        live.DEPhone,
		TargetExpectedTrip: live.TripID,
		TargetStoreID:      live.StoreID,
		FreeTarget:         free,
	}
	if err := s.trips.PersistMerge(ctx, persist); err != nil {
		if errors.Is(err, repository.ErrMergeConflict) {
			return ErrStaleMerge
		}
		return op.Fail(err)
	}
	if live.DEID != "" && built.trip.Status != models.TripStatusCompleted {
		s.sendTripUpdated(ctx, live.DEID, built.trip.TripID, live.TripID)
	}
	if free && live.DEPhone != "" {
		if de, derr := s.des.GetByPhone(ctx, live.DEPhone); derr == nil && de != nil {
			s.payIfNeeded(built.trip, de)
		}
	}
	return nil
}

type builtTrip struct {
	trip      *models.Trip
	haversine bool
}

func (s *BatchingService) buildSuccessor(ctx context.Context, target, source *models.Trip, orderID string, ds *models.Darkstore, targetDE *models.DeliveryExecutive) (*builtTrip, error) {
	pickups := pickupsSkippingOrder(target, orderID)
	var incomingPickups, incomingDrops []models.Task
	if source == nil {
		synth, err := s.synthesizeTasks(ctx, orderID, ds)
		if err != nil {
			return nil, err
		}
		for _, t := range synth {
			if t.Type == models.TaskTypePickup {
				incomingPickups = append(incomingPickups, t)
			} else {
				incomingDrops = append(incomingDrops, t)
			}
		}
	} else {
		incomingPickups = pickupsFrom(source, orderID)
		incomingDrops = dropsFrom(source, orderID)
	}

	pickups = append(pickups, incomingPickups...)

	var completedDrops, openDrops []models.Task
	seenDrop := map[string]bool{}
	appendDrops := func(trip *models.Trip, onlyOrder string) {
		if trip == nil {
			return
		}
		for i := range trip.Tasks {
			t := copyTask(trip.Tasks[i])
			stampTaskOrder(&t, trip)
			if t.Type != models.TaskTypeDrop {
				continue
			}
			if onlyOrder != "" && t.OrderID != onlyOrder {
				continue
			}
			if onlyOrder == "" && t.OrderID == orderID {
				continue
			}
			if seenDrop[t.OrderID] {
				continue
			}
			seenDrop[t.OrderID] = true
			if t.Status == models.TaskStatusCompleted {
				completedDrops = append(completedDrops, t)
			} else {
				openDrops = append(openDrops, t)
			}
		}
	}
	appendDrops(target, "")
	if source != nil {
		appendDrops(source, orderID)
	} else {
		for _, t := range incomingDrops {
			if seenDrop[t.OrderID] {
				continue
			}
			seenDrop[t.OrderID] = true
			openDrops = append(openDrops, t)
		}
	}

	originLat, originLng := ds.Latitude, ds.Longitude
	pinFirst := ""
	hasOpenPickup := false
	for _, p := range pickups {
		if p.Status != models.TaskStatusCompleted {
			hasOpenPickup = true
			break
		}
	}
	if !hasOpenPickup {
		if latest := latestCompletedDrop(append(append([]models.Task{}, completedDrops...), openDrops...)); latest != nil {
			originLat, originLng = latest.Lat, latest.Lng
		} else if first := firstOpenDrop(target.Tasks); first != nil {
			stampTaskOrder(first, target)
			pinFirst = first.TaskID
			for _, od := range openDrops {
				if od.OrderID == first.OrderID {
					pinFirst = od.TaskID
					originLat, originLng = od.Lat, od.Lng
					break
				}
			}
		}
	}

	tour := nearestNeighbourTour(ctx, s.distance, originLat, originLng, openDrops, pinFirst)

	tasks := append([]models.Task{}, pickups...)
	tasks = append(tasks, completedDrops...)
	tasks = append(tasks, tour.Drops...)
	resetReachedIfIncompletePickups(tasks)

	return s.assembleTrip(target, tasks, []string{target.TripID}, tour, ds, targetDE)
}

func (s *BatchingService) buildFromTasks(ctx context.Context, base *models.Trip, tasks []models.Task, ds *models.Darkstore, supersedes []string) (*builtTrip, error) {
	var pickups, completedDrops, openDrops []models.Task
	for _, t := range tasks {
		c := copyTask(t)
		stampTaskOrder(&c, base)
		switch c.Type {
		case models.TaskTypePickup:
			pickups = append(pickups, c)
		case models.TaskTypeDrop:
			if c.Status == models.TaskStatusCompleted {
				completedDrops = append(completedDrops, c)
			} else {
				openDrops = append(openDrops, c)
			}
		}
	}
	originLat, originLng := ds.Latitude, ds.Longitude
	pinFirst := ""
	hasOpenPickup := false
	for _, p := range pickups {
		if p.Status != models.TaskStatusCompleted {
			hasOpenPickup = true
			break
		}
	}
	if !hasOpenPickup {
		if latest := latestCompletedDrop(append(append([]models.Task{}, completedDrops...), openDrops...)); latest != nil {
			originLat, originLng = latest.Lat, latest.Lng
		} else if first := firstOpenDrop(openDrops); first != nil {
			pinFirst = first.TaskID
			originLat, originLng = first.Lat, first.Lng
		}
	}
	tour := nearestNeighbourTour(ctx, s.distance, originLat, originLng, openDrops, pinFirst)
	out := append([]models.Task{}, pickups...)
	out = append(out, completedDrops...)
	out = append(out, tour.Drops...)
	resetReachedIfIncompletePickups(out)
	return s.assembleTrip(base, out, supersedes, tour, ds, nil)
}

func (s *BatchingService) assembleTrip(base *models.Trip, tasks []models.Task, supersedes []string, tour tourResult, ds *models.Darkstore, targetDE *models.DeliveryExecutive) (*builtTrip, error) {
	tmp := &models.Trip{Tasks: tasks, OrderID: base.OrderID}
	orderIDs := pickupOrderIDs(tasks)
	if len(orderIDs) == 0 {
		orderIDs = base.LiveOrderIDs()
	}

	status := models.TripStatusAccepted
	if !tmp.HasIncompletePickup() {
		status = models.TripStatusOutForDelivery
	}
	if len(tmp.OpenDrops()) == 0 {
		status = models.TripStatusCompleted
	}

	var dropDeadline *int64
	if status == models.TripStatusOutForDelivery {
		dropDeadline = tmp.MinOpenDropDeadline()
	}

	now := timezone.Now()
	trip := &models.Trip{
		OrderIDs:          orderIDs,
		StoreID:           base.StoreID,
		DEID:              base.DEID,
		DEPhone:           base.DEPhone,
		Status:            status,
		Tasks:             tasks,
		Items:             tmp.ItemsRollup(),
		Payment:           tmp.PaymentRollup(),
		DistanceKM:        tour.KM,
		DistanceMethod:    tour.Method,
		SupersedesTripIDs: supersedes,
		CustomerUserID:    base.CustomerUserID,
		DropDeadline:      dropDeadline,
		CreatedAt:         now.UTC().Format(time.RFC3339),
	}
	if targetDE != nil {
		trip.DEID = targetDE.DEID
		trip.DEPhone = targetDE.PhoneNumber
	}
	trip.SyncIndexKeys()
	if status == models.TripStatusCompleted {
		ts := now.UTC().Format(time.RFC3339)
		trip.CompletedAt = ts
	}

	if s.payoutCfg != nil {
		pcfg, err := s.payoutCfg.Get(context.Background())
		if err == nil && pcfg != nil {
			trip.BasePayZMW = computeBasePay(trip.DistanceKM, pcfg)
			stampAssignmentDecision(trip, pcfg, now, s.fareEngine)
			trip.BasePayZMW = computeBasePay(trip.DistanceKM, pcfg)
		}
	}
	return &builtTrip{trip: trip, haversine: tour.Haversine}, nil
}

func (s *BatchingService) synthesizeTasks(ctx context.Context, orderID string, ds *models.Darkstore) ([]models.Task, error) {
	if s.java == nil {
		return nil, fmt.Errorf("java client required to synthesize tasks for %s", orderID)
	}
	order, err := s.java.GetOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order == nil {
		return nil, fmt.Errorf("order %s not found", orderID)
	}
	order.OrderID = order.EffectiveOrderID()
	if order.OrderID == "" {
		order.OrderID = orderID
	}
	trip := buildTripFromOrder(*order, "", "", "", order.OrderID, ds.DarkstoreID, 0, 0, ds)
	for i := range trip.Tasks {
		trip.Tasks[i].OrderID = order.OrderID
		if trip.Tasks[i].Type == models.TaskTypePickup {
			trip.Tasks[i].Items = trip.Items
		}
		if trip.Tasks[i].Type == models.TaskTypeDrop {
			trip.Tasks[i].Payment = trip.Payment
		}
	}
	return trip.Tasks, nil
}

func (s *BatchingService) orderStoreID(ctx context.Context, orderID string, source *models.Trip) (string, error) {
	if source != nil && source.StoreID != "" {
		return source.StoreID, nil
	}
	if s.java == nil {
		return "", fmt.Errorf("cannot resolve store for order %s", orderID)
	}
	o, err := s.java.GetOrder(ctx, orderID)
	if err != nil {
		return "", err
	}
	if o == nil || o.StoreID == "" {
		return "", fmt.Errorf("order %s has no storeId", orderID)
	}
	return o.StoreID, nil
}

func (s *BatchingService) sendTripUpdated(ctx context.Context, deID, newTripID, supersedesTripID string) {
	if s.notifier == nil || deID == "" {
		return
	}
	s.notifier.Send(ctx, models.NotificationSendRequest{
		RecipientType: models.RecipientTypeDriver,
		RecipientID:   deID,
		EventType:     "TRIP_UPDATED",
		Priority:      models.PriorityNormal,
		Title:         "Trip updated",
		Body:          "Your delivery list has changed.",
		Data: map[string]string{
			"type":               "TRIP_UPDATED",
			"trip_id":            newTripID,
			"supersedes_trip_id": supersedesTripID,
		},
	})
}

func (s *BatchingService) payIfNeeded(trip *models.Trip, de *models.DeliveryExecutive) {
	if s.payout == nil || trip == nil || de == nil {
		return
	}
	s.payout.OnTripCompleted(context.Background(), trip, de.PhoneNumber)
}

func checkExpectedUpdatedAt(expected map[string]string, trips ...*models.Trip) error {
	for _, t := range trips {
		if t == nil {
			continue
		}
		if expected == nil {
			return ErrStaleMerge
		}
		want, ok := expected[t.TripID]
		if !ok || want != t.UpdatedAt {
			return ErrStaleMerge
		}
	}
	return nil
}

func expectedAt(m map[string]string, tripID, fallback string) string {
	if m != nil {
		if v, ok := m[tripID]; ok {
			return v
		}
	}
	return fallback
}

func pickupsFrom(trip *models.Trip, onlyOrder string) []models.Task {
	var out []models.Task
	if trip == nil {
		return out
	}
	for i := range trip.Tasks {
		if trip.Tasks[i].Type != models.TaskTypePickup {
			continue
		}
		t := copyTask(trip.Tasks[i])
		stampTaskOrder(&t, trip)
		if onlyOrder != "" && t.OrderID != onlyOrder {
			continue
		}
		out = append(out, t)
	}
	return out
}

func dropsFrom(trip *models.Trip, onlyOrder string) []models.Task {
	var out []models.Task
	if trip == nil {
		return out
	}
	for i := range trip.Tasks {
		if trip.Tasks[i].Type != models.TaskTypeDrop {
			continue
		}
		t := copyTask(trip.Tasks[i])
		stampTaskOrder(&t, trip)
		if onlyOrder != "" && t.OrderID != onlyOrder {
			continue
		}
		out = append(out, t)
	}
	return out
}

func tasksExcludingOrder(trip *models.Trip, orderID string) []models.Task {
	var out []models.Task
	for i := range trip.Tasks {
		t := copyTask(trip.Tasks[i])
		stampTaskOrder(&t, trip)
		if t.OrderID == orderID {
			continue
		}
		out = append(out, t)
	}
	return out
}

func dropOrPickupOrders(tasks []models.Task) []string {
	seen := map[string]bool{}
	var ids []string
	for _, t := range tasks {
		if t.OrderID == "" || seen[t.OrderID] {
			continue
		}
		seen[t.OrderID] = true
		ids = append(ids, t.OrderID)
	}
	return ids
}

func pickupsSkippingOrder(trip *models.Trip, skipOrder string) []models.Task {
	var out []models.Task
	if trip == nil {
		return out
	}
	for i := range trip.Tasks {
		if trip.Tasks[i].Type != models.TaskTypePickup {
			continue
		}
		t := copyTask(trip.Tasks[i])
		stampTaskOrder(&t, trip)
		if skipOrder != "" && t.OrderID == skipOrder {
			continue
		}
		out = append(out, t)
	}
	return out
}

func pickupOrderIDs(tasks []models.Task) []string {
	seen := map[string]bool{}
	var ids []string
	for _, t := range tasks {
		if t.Type != models.TaskTypePickup || t.OrderID == "" || seen[t.OrderID] {
			continue
		}
		seen[t.OrderID] = true
		ids = append(ids, t.OrderID)
	}
	return ids
}
