package service

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

const (
	defaultStoreID  = "221"
	cronInterval    = 10 * time.Second
	cronLockTTLSecs = 30

	// eligibleOrderStatus is the Java order status that makes an order eligible
	// for rider assignment. Only fully-packed orders should enter the delivery pipeline.
	eligibleOrderStatus = "READY_FOR_DELIVERY"

	// recipientFallback is used for the drop task when the order has no
	// delivery name yet (Java may send it absent/empty for now).
	recipientFallback = "Shivang Awasthi"
)

type AssignmentCron struct {
	tripRepo             *repository.TripRepository
	deRepo               *repository.DERepository
	cronLockRepo         *repository.CronLockRepository
	payoutConfigRepo     *repository.PayoutConfigRepository
	assignmentConfigRepo *repository.AssignmentConfigRepository
	cashConfigRepo       *repository.CashConfigRepository
	darkstoreRepo        *repository.DarkstoreRepository
	javaClient           *JavaOrderClient
	distanceService      *DistanceService
	fareEngine           *FareEngine
	notifier             NotificationService
	logger               *logrus.Logger

	wg     sync.WaitGroup
	stopCh chan struct{}
}

func NewAssignmentCron(
	tripRepo *repository.TripRepository,
	deRepo *repository.DERepository,
	cronLockRepo *repository.CronLockRepository,
	payoutConfigRepo *repository.PayoutConfigRepository,
	assignmentConfigRepo *repository.AssignmentConfigRepository,
	cashConfigRepo *repository.CashConfigRepository,
	darkstoreRepo *repository.DarkstoreRepository,
	javaClient *JavaOrderClient,
	distanceService *DistanceService,
	fareEngine *FareEngine,
	notifier NotificationService,
	logger *logrus.Logger,
) *AssignmentCron {
	return &AssignmentCron{
		tripRepo:             tripRepo,
		deRepo:               deRepo,
		cronLockRepo:         cronLockRepo,
		payoutConfigRepo:     payoutConfigRepo,
		assignmentConfigRepo: assignmentConfigRepo,
		cashConfigRepo:       cashConfigRepo,
		darkstoreRepo:        darkstoreRepo,
		javaClient:           javaClient,
		distanceService:      distanceService,
		fareEngine:           fareEngine,
		notifier:             notifier,
		logger:               logger,
		stopCh:               make(chan struct{}),
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

	acfg, err := c.assignmentConfigRepo.Get(ctx)
	if err != nil {
		c.logger.WithError(err).Warn("assignment cron: failed to fetch assignment config — using default accept window")
		acfg = &models.AssignmentConfig{}
	}
	autoRejectSecs := acfg.EffectiveAutoRejectSeconds()

	// 2. Fetch READY_FOR_DELIVERY orders from Java
	orders, err := c.javaClient.GetReadyForDeliveryOrders(ctx, defaultStoreID)
	if err != nil {
		c.logger.WithError(err).Error("assignment cron: failed to fetch READY_FOR_DELIVERY orders — skipping tick")
		return
	}

	// 3. Fetch eligible DEs (FIFO)
	eligibleDEs, err := c.deRepo.FindEligibleByStoreFIFO(ctx, defaultStoreID)
	if err != nil {
		c.logger.WithError(err).Error("assignment cron: failed to fetch eligible DEs — skipping tick")
		return
	}

	cashCfg, err := c.cashConfigRepo.Get(ctx)
	if err != nil {
		c.logger.WithError(err).Error("assignment cron: failed to fetch cash config — skipping tick")
		return
	}
	cashLimit := cashCfg.EffectiveLimitZMW()

	// 4. For each READY_FOR_DELIVERY order: check trip existence in parallel
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
			orderID := o.EffectiveOrderID()
			if orderID == "" {
				c.logger.Error("assignment cron: skipping order with no orderId or orderNumber")
				return
			}
			o.OrderID = orderID
			existing, err := c.tripRepo.GetByOrderID(ctx, orderID)
			if err != nil {
				c.logger.WithError(err).WithField("order_id", orderID).
					Error("assignment cron: failed to check trip existence")
				return
			}
			results[idx] = orderResult{order: o, trip: existing, needsCreate: existing == nil}
		}(i, order)
	}
	wg.Wait()

	// 5. Detect cancellations: check active trips whose order is no longer READY_FOR_DELIVERY
	c.detectCancellations(ctx, orders)

	// 5b. Auto-reject trips whose accept window expired — revert to pool so the
	// assign step below re-offers them (to a different DE, since the rejecter is
	// now in rejected_de_ids).
	now := timezone.Now()
	for i := range results {
		t := results[i].trip
		if t == nil || !isAcceptExpired(t, now) {
			continue
		}
		if err := c.tripRepo.RejectToPool(ctx, t.TripID, t.DEPhone, t.StoreID, t.DEID); err != nil {
			c.logger.WithError(err).WithField("trip_id", t.TripID).
				Warn("assignment cron: auto-reject failed — will retry next tick")
			continue
		}
		c.logger.WithFields(logrus.Fields{"trip_id": t.TripID, "de_id": t.DEID}).
			Info("assignment cron: auto-rejected expired trip")
		// Reflect the new pool state locally so the assign loop picks it up this
		// tick and skips the DE that just lost it.
		t.RejectedDEIDs = append(t.RejectedDEIDs, t.DEID)
		t.Status = models.TripStatusCreated
		t.DEID = ""
		t.DEPhone = ""
		t.AcceptDeadline = ""
	}

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

	// Assign each unassigned trip to the first eligible DE that has not already
	// rejected it and is not already taken this tick.
	usedDE := make(map[string]bool)
	for _, trip := range unassigned {
		for _, de := range eligibleDEs {
			if usedDE[de.DEID] || trip.HasRejected(de.DEID) || de.CashExceeds(cashLimit) {
				continue
			}
			assignedAt := now
			deadline := assignedAt.Add(time.Duration(autoRejectSecs) * time.Second).Format(time.RFC3339)
			stampAssignmentDecision(trip, cfg, assignedAt, c.fareEngine)
			trip.AssignedAt = assignedAt.Format(time.RFC3339)
			trip.AcceptDeadline = deadline
			trip.DEID = de.DEID
			trip.DEPhone = de.PhoneNumber
			trip.Status = models.TripStatusAssigned

			if err := c.tripRepo.Assign(
				ctx,
				trip.TripID,
				trip.OrderID,
				de.DEID,
				de.PhoneNumber,
				trip.AssignedAt,
				deadline,
				trip.SLAMinutes,
				trip.RateRuleID,
				trip.RateRuleVersion,
				trip.RateMultiplier,
				trip.RateFlatZMW,
			); err != nil {
				c.logger.WithError(err).WithFields(logrus.Fields{
					"trip_id": trip.TripID, "de_id": de.DEID,
				}).Warn("assignment cron: assign conflict — trip or DE taken, trying next DE")
				continue
			}
			usedDE[de.DEID] = true
			c.logger.WithFields(logrus.Fields{
				"trip_id": trip.TripID, "de_id": de.DEID,
			}).Info("assignment cron: trip assigned")
			// Best-effort push: never block the tick. Capture loop vars; use a
			// detached context so cancellation of the tick can't kill the send.
			assignedDE := de
			assignedTrip := trip
			go func() {
				c.notifier.Send(context.Background(), models.NotificationSendRequest{
					RecipientType: models.RecipientTypeDriver,
					RecipientID:   assignedDE.DEID,
					EventType:     "ORDER_ASSIGNED",
					Priority:      models.PriorityCritical,
					Title:         "New order!",
					Body:          "Tap to view your trip.",
					Data: map[string]string{
						"trip_id":         assignedTrip.TripID,
						"order_id":        assignedTrip.OrderID,
						"accept_deadline": assignedTrip.AcceptDeadline,
					},
				})
			}()
			break
		}
	}
}

func stampAssignmentDecision(trip *models.Trip, cfg *models.PayoutConfig, assignedAt time.Time, fareEngine *FareEngine) {
	if trip == nil || cfg == nil {
		return
	}

	basePay := computeBasePay(trip.DistanceKM, cfg)
	decision := RateDecision{Multiplier: 1, FlatZMW: 0}
	if fareEngine != nil {
		decision = fareEngine.ResolveRate(assignedAt, basePay)
	}

	trip.SLAMinutes = trip.DistanceKM * cfg.EffectiveMinutesPerKm()
	trip.RateRuleID = decision.RuleID
	trip.RateRuleVersion = decision.Version
	trip.RateMultiplier = decision.Multiplier
	trip.RateFlatZMW = decision.FlatZMW
}

func (c *AssignmentCron) createTrip(ctx context.Context, order JavaOrder, cfg *models.PayoutConfig) (*models.Trip, error) {
	orderID := order.EffectiveOrderID()
	if orderID == "" {
		return nil, fmt.Errorf("order has no orderId or orderNumber")
	}
	order.OrderID = orderID

	storeID := order.StoreID
	if storeID == "" {
		storeID = defaultStoreID
	}

	ds, err := c.darkstoreRepo.GetByID(ctx, storeID)
	if err != nil {
		return nil, fmt.Errorf("darkstore lookup failed for store %s: %w", storeID, err)
	}
	if ds == nil {
		return nil, fmt.Errorf("darkstore not found for store %s", storeID)
	}
	if !ds.IsActive {
		return nil, fmt.Errorf("darkstore %s is inactive", storeID)
	}

	distKM, err := c.distanceService.DistanceKM(ctx, ds.Latitude, ds.Longitude, order.Delivery.Lat, order.Delivery.Lng)
	if err != nil {
		// Permanent no-route (ZERO_RESULTS / NOT_FOUND): persist a terminal
		// distance_failed trip so the order disappears from the needs-create set
		// and is never re-billed to the Distance Matrix API. Returns (nil, nil):
		// the order is handled, but there is no assignable trip.
		if errors.Is(err, ErrNoRoute) {
			failed := buildTripFromOrder(order, "", "", "", orderID, storeID, 0, 0, ds)
			failed.Status = models.TripStatusDistanceFailed
			if perr := c.tripRepo.Create(ctx, failed); perr != nil {
				return nil, fmt.Errorf("failed to persist distance_failed trip for order %s: %w", orderID, perr)
			}
			c.logger.WithError(err).WithFields(logrus.Fields{
				"order_id": orderID,
				"store_id": storeID,
				"origin":   fmt.Sprintf("%.4f,%.4f", ds.Latitude, ds.Longitude),
				"dest":     fmt.Sprintf("%.4f,%.4f", order.Delivery.Lat, order.Delivery.Lng),
			}).Warn("createTrip: no drivable route — marked trip distance_failed (will not retry)")
			return nil, nil
		}
		return nil, fmt.Errorf("distance lookup failed for order %s: %w", orderID, err)
	}

	basePayZMW := computeBasePay(distKM, cfg)

	// IDs are assigned by TripRepository.Create at the persistence boundary.
	var tripID, pickupTaskID, dropTaskID string

	if order.PaymentMethod != "" && !isKnownPaymentMethod(order.PaymentMethod) {
		c.logger.WithFields(logrus.Fields{
			"order_id":       orderID,
			"payment_method": order.PaymentMethod,
		}).Warn("createTrip: unrecognized paymentMethod, treating as online (collect no cash)")
	}

	trip := buildTripFromOrder(order, tripID, pickupTaskID, dropTaskID, orderID, storeID, distKM, basePayZMW, ds)

	if err := c.tripRepo.Create(ctx, trip); err != nil {
		return nil, fmt.Errorf("failed to persist trip for order %s: %w", orderID, err)
	}
	return trip, nil
}

// detectCancellations checks active trips for orders that Java has marked
// CANCELLED so the trip can be cancelled and the DE freed.
//
// A full cancellation check would query all active trips and verify each
// against Java. For now we rely on the cron seeing orders disappear from the
// READY_FOR_DELIVERY list — an order leaves that list either by cancellation
// or by progressing to OUT_FOR_DELIVERY on rider pickup. The DynamoDB
// transaction protects correctness. This is a best-effort stub completed in a
// later phase.
func (c *AssignmentCron) detectCancellations(ctx context.Context, currentReadyOrders []JavaOrder) {
	_ = ctx
	_ = currentReadyOrders
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

// buildTripFromOrder constructs a Trip value from a normalized JavaOrder and the
// pre-computed identifiers and metrics that createTrip resolves via I/O. It is
// a pure field-mapping helper with no I/O so that it can be unit-tested.
func buildTripFromOrder(
	order JavaOrder,
	tripID, pickupTaskID, dropTaskID, orderID, storeID string,
	distKM, basePayZMW float64,
	ds *models.Darkstore,
) *models.Trip {
	return &models.Trip{
		TripID:         tripID,
		OrderID:        orderID,
		StoreID:        storeID,
		Status:         models.TripStatusCreated,
		DistanceKM:     distKM,
		BasePayZMW:     basePayZMW,
		CustomerUserID: order.CustomerID,
		Items:          tripItemsFromOrder(order),
		Payment:        paymentFromOrder(order),
		Tasks: []models.Task{
			{
				TaskID:  pickupTaskID,
				Type:    models.TaskTypePickup,
				Status:  models.TaskStatusCreated,
				Address: ds.Name,
				Lat:     ds.Latitude,
				Lng:     ds.Longitude,
			},
			{
				TaskID:        dropTaskID,
				Type:          models.TaskTypeDrop,
				Status:        models.TaskStatusCreated,
				Phone:         order.Delivery.Phone,
				Address:       order.Delivery.Address,
				Lat:           order.Delivery.Lat,
				Lng:           order.Delivery.Lng,
				OTP:           randomOTP(),
				RecipientName: firstNonEmpty(order.Delivery.Name, recipientFallback),
			},
		},
	}
}

// tripItemsFromOrder maps the Java order's items onto trip items. ImageURL is
// stored as the bare R2 key from Java — callers are responsible for any URL
// building, so it is passed through untouched.
func tripItemsFromOrder(order JavaOrder) []models.TripItem {
	if len(order.Items) == 0 {
		return nil
	}
	items := make([]models.TripItem, 0, len(order.Items))
	for _, it := range order.Items {
		items = append(items, models.TripItem{
			Name:     it.ProductName,
			ImageURL: it.ImageURL,
			Quantity: it.Quantity,
			Sku:      it.Sku,
		})
	}
	return items
}

// paymentMethodCOD is the order-service wire value for cash-on-delivery — the
// only method for which the driver collects cash at drop-off.
const paymentMethodCOD = "COD"

// knownOnlinePaymentMethods are paymentMethod wire values that mean the order
// was already paid for online, so the driver collects nothing.
var knownOnlinePaymentMethods = map[string]bool{
	"AIRTEL_MONEY":  true,
	"CARD":          true,
	"BANK_TRANSFER": true,
}

// paymentFromOrder maps the Java order's payment fields onto a trip Payment.
// CollectCash is true only for explicit cash-on-delivery ("COD"); any unknown
// or empty method is treated as online (collect nothing); callers should
// warn-log unrecognized non-empty methods — this function does not.
func paymentFromOrder(order JavaOrder) *models.Payment {
	return &models.Payment{
		CollectCash: order.PaymentMethod == paymentMethodCOD,
		AmountZMW:   order.GrandTotal,
		Currency:    order.Currency,
	}
}

// isKnownPaymentMethod reports whether a paymentMethod wire value is one qcom
// recognizes ("COD" or a known online method).
func isKnownPaymentMethod(method string) bool {
	return method == paymentMethodCOD || knownOnlinePaymentMethods[method]
}

// firstNonEmpty returns a if it is non-blank (ignoring surrounding whitespace),
// otherwise b.
func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// randomOTP returns a random 4-digit OTP string in the range "1000".."9999".
func randomOTP() string {
	return fmt.Sprintf("%04d", rand.Intn(9000)+1000)
}

// isAcceptExpired reports whether an assigned trip's accept window has elapsed
// and it should be auto-rejected back into the pool.
func isAcceptExpired(trip *models.Trip, now time.Time) bool {
	if trip.Status != models.TripStatusAssigned || trip.AcceptDeadline == "" {
		return false
	}
	deadline, err := time.Parse(time.RFC3339, trip.AcceptDeadline)
	if err != nil {
		return false
	}
	return now.After(deadline)
}
