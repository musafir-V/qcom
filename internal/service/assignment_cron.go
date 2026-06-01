package service

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

const (
	defaultStoreID  = "112233"
	cronInterval    = 10 * time.Second
	cronLockTTLSecs = 30
)

type AssignmentCron struct {
	tripRepo         *repository.TripRepository
	deRepo           *repository.DERepository
	cronLockRepo     *repository.CronLockRepository
	payoutConfigRepo *repository.PayoutConfigRepository
	javaClient       *JavaOrderClient
	distanceService  *DistanceService
	logger           *logrus.Logger

	wg     sync.WaitGroup
	stopCh chan struct{}
}

func NewAssignmentCron(
	tripRepo *repository.TripRepository,
	deRepo *repository.DERepository,
	cronLockRepo *repository.CronLockRepository,
	payoutConfigRepo *repository.PayoutConfigRepository,
	javaClient *JavaOrderClient,
	distanceService *DistanceService,
	logger *logrus.Logger,
) *AssignmentCron {
	return &AssignmentCron{
		tripRepo:         tripRepo,
		deRepo:           deRepo,
		cronLockRepo:     cronLockRepo,
		payoutConfigRepo: payoutConfigRepo,
		javaClient:       javaClient,
		distanceService:  distanceService,
		logger:           logger,
		stopCh:           make(chan struct{}),
	}
}

// Start launches the cron ticker in a background goroutine.
// Call Stop() to drain and shut down cleanly.
func (c *AssignmentCron) Start() {
	c.logger.Info("assignment cron: starting")
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(cronInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.runTick()
			case <-c.stopCh:
				c.logger.Info("assignment cron: stopped")
				return
			}
		}
	}()
}

// Stop signals the cron to stop and waits for any in-progress tick to complete.
func (c *AssignmentCron) Stop() {
	close(c.stopCh)
	c.wg.Wait()
}

func (c *AssignmentCron) runTick() {
	// Panic recovery — log only. The deferred lock-release below already runs
	// on panic via LIFO defer ordering, so we must not release again here
	// (a double-release could delete another instance's freshly-acquired lock).
	defer func() {
		if r := recover(); r != nil {
			c.logger.WithField("panic", r).Error("assignment cron: panic recovered")
		}
	}()

	ctx := context.Background()

	// Acquire distributed lock
	acquired, err := c.cronLockRepo.Acquire(ctx, cronLockTTLSecs)
	if err != nil {
		c.logger.WithError(err).Error("assignment cron: failed to acquire lock")
		return
	}
	if !acquired {
		return // another instance is running
	}
	defer func() {
		// Retry release up to 3 times
		for i := 0; i < 3; i++ {
			if err := c.cronLockRepo.Release(ctx); err == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		c.logger.Error("assignment cron: failed to release lock after 3 attempts")
	}()

	start := time.Now()
	c.tick(ctx)
	elapsed := time.Since(start)

	if elapsed > 20*time.Second {
		c.logger.WithField("elapsed_ms", elapsed.Milliseconds()).
			Warn("assignment cron: tick took >20s — investigate Maps/DynamoDB latency")
	}
}

func (c *AssignmentCron) tick(ctx context.Context) {
	// 1. Fetch payout config (used for base_pay computation)
	cfg, err := c.payoutConfigRepo.Get(ctx)
	if err != nil {
		c.logger.WithError(err).Error("assignment cron: failed to fetch payout config — skipping tick")
		return
	}

	// 2. Fetch PACKING orders from Java
	orders, err := c.javaClient.GetPackingOrders(ctx, defaultStoreID)
	if err != nil {
		c.logger.WithError(err).Error("assignment cron: failed to fetch PACKING orders — skipping tick")
		return
	}

	// 3. Fetch eligible DEs (FIFO)
	eligibleDEs, err := c.deRepo.FindEligibleByStoreFIFO(ctx, defaultStoreID)
	if err != nil {
		c.logger.WithError(err).Error("assignment cron: failed to fetch eligible DEs — skipping tick")
		return
	}

	// 4. For each PACKING order: check trip existence in parallel
	type orderResult struct {
		order       JavaOrder
		trip        *models.Trip
		needsCreate bool
	}
	results := make([]orderResult, len(orders))
	var wg sync.WaitGroup
	for i, order := range orders {
		wg.Add(1)
		go func(idx int, o JavaOrder) {
			defer wg.Done()
			existing, err := c.tripRepo.GetByOrderID(ctx, o.OrderID)
			if err != nil {
				c.logger.WithError(err).WithField("order_id", o.OrderID).
					Error("assignment cron: failed to check trip existence")
				return
			}
			results[idx] = orderResult{order: o, trip: existing, needsCreate: existing == nil}
		}(i, order)
	}
	wg.Wait()

	// 5. Detect cancellations: check active trips whose order is no longer PACKING
	c.detectCancellations(ctx, orders)

	// 6. Create missing trips (parallel Maps calls)
	var newTrips []*models.Trip
	type createResult struct {
		trip *models.Trip
		err  error
	}
	createCh := make(chan createResult, len(results))

	for _, r := range results {
		if !r.needsCreate {
			continue
		}
		wg.Add(1)
		go func(o JavaOrder) {
			defer wg.Done()
			trip, err := c.createTrip(ctx, o, cfg)
			createCh <- createResult{trip: trip, err: err}
		}(r.order)
	}
	wg.Wait()
	close(createCh)

	for cr := range createCh {
		if cr.err != nil {
			c.logger.WithError(cr.err).Warn("assignment cron: failed to create trip — will retry next tick")
			continue
		}
		if cr.trip != nil {
			newTrips = append(newTrips, cr.trip)
		}
	}

	// 7. Assign: collect all unassigned trips (newly created + pre-existing created status)
	var unassigned []*models.Trip
	for _, r := range results {
		if r.trip != nil && r.trip.Status == models.TripStatusCreated && r.trip.DEID == "" {
			unassigned = append(unassigned, r.trip)
		}
	}
	unassigned = append(unassigned, newTrips...)

	// FIFO: sort unassigned by created_at ascending (oldest first)
	sortTripsByCreatedAt(unassigned)

	// Assign one DE per unassigned trip
	deIdx := 0
	for _, trip := range unassigned {
		if deIdx >= len(eligibleDEs) {
			break // no more DEs available
		}
		de := eligibleDEs[deIdx]
		deIdx++

		if err := c.tripRepo.Assign(ctx, trip.TripID, trip.OrderID, de.DEID, de.PhoneNumber, timezone.Now().Format(time.RFC3339)); err != nil {
			c.logger.WithError(err).WithFields(logrus.Fields{
				"trip_id": trip.TripID, "de_id": de.DEID,
			}).Warn("assignment cron: assign conflict — DE or trip taken, skipping")
			deIdx-- // DE may still be available; retry with next trip
		} else {
			c.logger.WithFields(logrus.Fields{
				"trip_id": trip.TripID, "de_id": de.DEID,
			}).Info("assignment cron: trip assigned")
		}
	}
}

func (c *AssignmentCron) createTrip(ctx context.Context, order JavaOrder, cfg *models.PayoutConfig) (*models.Trip, error) {
	// Get darkstore coordinates from DarkstoreRepository would go here.
	// For now, use placeholder darkstore coordinates for store 112233.
	// Phase 3 wires in real darkstore lat/lng via DarkstoreRepository.
	storeLat := -15.4167 // Lusaka approximate
	storeLng := 28.2833

	distKM, err := c.distanceService.DistanceKM(ctx, storeLat, storeLng, order.Delivery.Lat, order.Delivery.Lng)
	if err != nil {
		return nil, fmt.Errorf("distance lookup failed for order %s: %w", order.OrderID, err)
	}

	basePayZMW := cfg.RatePerKmZMW * distKM

	storeID := order.StoreID
	if storeID == "" {
		storeID = defaultStoreID
	}

	tripID := uuid.New().String()
	pickupTaskID := uuid.New().String()
	dropTaskID := uuid.New().String()

	trip := &models.Trip{
		TripID:     tripID,
		OrderID:    order.OrderID,
		StoreID:    storeID,
		Status:     models.TripStatusCreated,
		DistanceKM: distKM,
		BasePayZMW: basePayZMW,
		Tasks: []models.Task{
			{
				TaskID:  pickupTaskID,
				Type:    models.TaskTypePickup,
				Status:  models.TaskStatusArrived, // auto-arrived at creation
				Phone:   darkstorePhone(storeID),
				Address: darkstoreAddress(storeID),
				Lat:     storeLat,
				Lng:     storeLng,
			},
			{
				TaskID:  dropTaskID,
				Type:    models.TaskTypeDrop,
				Status:  models.TaskStatusCreated,
				Phone:   order.Delivery.Phone,
				Address: order.Delivery.Address,
				Lat:     order.Delivery.Lat,
				Lng:     order.Delivery.Lng,
				OTP:     randomOTP(),
			},
		},
	}

	if err := c.tripRepo.Create(ctx, trip); err != nil {
		return nil, fmt.Errorf("failed to persist trip for order %s: %w", order.OrderID, err)
	}
	return trip, nil
}

// detectCancellations checks active trips for orders that Java has marked
// CANCELLED so the trip can be cancelled and the DE freed.
//
// A full cancellation check would query all active trips and verify each
// against Java. For now we rely on the cron seeing orders disappear from the
// PACKING list — the DynamoDB transaction protects correctness. This is a
// best-effort stub completed in a later phase.
func (c *AssignmentCron) detectCancellations(ctx context.Context, currentPackingOrders []JavaOrder) {
	_ = ctx
	_ = currentPackingOrders
}

// sortTripsByCreatedAt sorts trips ascending by created_at (oldest first = FIFO).
// Insertion sort keeps the relative order of trips with equal timestamps stable.
func sortTripsByCreatedAt(trips []*models.Trip) {
	for i := 1; i < len(trips); i++ {
		for j := i; j > 0 && trips[j].CreatedAt < trips[j-1].CreatedAt; j-- {
			trips[j], trips[j-1] = trips[j-1], trips[j]
		}
	}
}

// randomOTP returns a random 4-digit OTP string in the range "1000".."9999".
func randomOTP() string {
	return fmt.Sprintf("%04d", rand.Intn(9000)+1000)
}

// darkstorePhone and darkstoreAddress return placeholder values for store 112233.
// Replace with DarkstoreRepository lookup when multi-store support is added.
func darkstorePhone(storeID string) string   { return "+260977000001" }
func darkstoreAddress(storeID string) string { return "Bunzo Darkstore, Lusaka" }
