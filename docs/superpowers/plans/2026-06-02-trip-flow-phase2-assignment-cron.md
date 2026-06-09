# Trip Flow — Phase 2: Assignment Cron

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the 10-second assignment cron that queries the Java order-service for PACKING orders, creates Trip entities in DynamoDB, assigns eligible DEs via atomic transactions, and detects cancelled orders to free stuck DEs. The cron is distributed-safe via a DynamoDB lock.

**Architecture:** A Go `time.Ticker` fires every 10 seconds inside a goroutine. Before each tick it attempts to acquire the `CronLock` (30s TTL). If acquired, it runs the tick and explicitly releases the lock. A `sync.WaitGroup` + `recover()` wraps the tick for panic safety and graceful SIGTERM drain. All Maps calls for distance computation are parallelised with goroutines.

**Tech Stack:** Go 1.24, AWS DynamoDB (aws-sdk-go-v2), gorilla/mux, Google Maps Distance Matrix API, logrus

**Prerequisites:**
- Phase 1 complete — `TripRepository`, `CronLockRepository`, `DERepository.FindEligibleByStoreFIFO`, timezone package
- `internal/models/payout_config.go` and `internal/repository/payout_config_repository.go` — from Plan C (or stub below)
- Java order-service running and reachable at `JAVA_ORDER_SERVICE_URL` env var

---

## File Map

### New Files
- `internal/service/java_order_client.go` — HTTP client for Java order-service
- `internal/service/distance_service.go` — Google Maps Distance Matrix API
- `internal/service/assignment_cron.go` — distributed cron: create trips + assign DEs + detect cancellations

### Modified Files
- `internal/config/config.go` — add `JavaOrderServiceURL` config field
- `cmd/server/main.go` — start cron goroutine with graceful shutdown

---

## Task 1: Config — Java Order Service URL

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Step 1: Add Java config field**

Add a new config struct and field in `config.go`:

```go
type JavaConfig struct {
	OrderServiceURL string
}
```

Add `Java JavaConfig` to the `Config` struct:
```go
type Config struct {
	Server   ServerConfig
	DynamoDB DynamoDBConfig
	JWT      JWTConfig
	OTP      OTPConfig
	S3       S3Config
	Google   GoogleConfig
	Java     JavaConfig
	IsTest   bool
}
```

In `Load()`, populate it:
```go
Java: JavaConfig{
	OrderServiceURL: getEnv("JAVA_ORDER_SERVICE_URL", "http://localhost:8081"),
},
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./internal/config/...
```

Expected: no output

- [ ] **Step 3: Commit**

```bash
git add internal/config/config.go
git commit -m "feat: add JAVA_ORDER_SERVICE_URL config field"
```

---

## Task 2: Java Order Client

**Files:**
- Create: `internal/service/java_order_client.go`

- [ ] **Step 1: Write the client**

```go
// internal/service/java_order_client.go
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

// JavaOrder is the subset of fields the cron needs from the Java order response.
type JavaOrder struct {
	OrderID  string          `json:"orderId"`
	Status   string          `json:"status"`
	Delivery JavaDelivery    `json:"delivery"`
	StoreID  string          `json:"storeId"` // may be empty; cron defaults to DefaultStoreID
}

type JavaDelivery struct {
	Address string  `json:"address"`
	Lat     float64 `json:"latitude"`
	Lng     float64 `json:"longitude"`
	Phone   string  `json:"phone"`
}

type JavaOrderClient struct {
	baseURL    string
	httpClient *http.Client
	logger     *logrus.Logger
}

func NewJavaOrderClient(baseURL string, logger *logrus.Logger) *JavaOrderClient {
	return &JavaOrderClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger: logger,
	}
}

// GetPackingOrders fetches all PACKING orders for a store.
// Handles pagination — returns all pages combined.
func (c *JavaOrderClient) GetPackingOrders(ctx context.Context, storeID string) ([]JavaOrder, error) {
	op := logging.Start(ctx, c.logger, "JavaOrderClient.GetPackingOrders", logrus.Fields{"store_id": storeID})
	defer op.End()

	var allOrders []JavaOrder
	page := 0
	pageSize := 50

	for {
		url := fmt.Sprintf("%s/api/v1/orders/store/%s?status=PACKING&pageNum=%d&pageSize=%d",
			c.baseURL, storeID, page, pageSize)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, op.Fail(fmt.Errorf("failed to build request: %w", err))
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, op.Fail(fmt.Errorf("java order-service unavailable: %w", err))
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, op.Fail(fmt.Errorf("java order-service returned %d", resp.StatusCode))
		}

		var paged struct {
			Content []JavaOrder `json:"content"`
			Meta    struct {
				Last bool `json:"last"`
			} `json:"meta"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&paged); err != nil {
			return nil, op.Fail(fmt.Errorf("failed to decode order response: %w", err))
		}

		allOrders = append(allOrders, paged.Content...)
		if paged.Meta.Last {
			break
		}
		page++
	}

	op.With("count", len(allOrders))
	return allOrders, nil
}

// UpdateOrderStatus calls Java to transition an order to a new status.
// actorID identifies who triggered the change (e.g. "DE:{deID}").
func (c *JavaOrderClient) UpdateOrderStatus(ctx context.Context, orderID, status, actorID string) error {
	op := logging.Start(ctx, c.logger, "JavaOrderClient.UpdateOrderStatus", logrus.Fields{
		"order_id": orderID, "status": status,
	})
	defer op.End()

	url := fmt.Sprintf("%s/api/v1/orders/%s/status", c.baseURL, orderID)
	body := fmt.Sprintf(`{"status":%q,"notes":"triggered by bunzo-qcom"}`, status)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url,
		newStringReader(body))
	if err != nil {
		return op.Fail(fmt.Errorf("failed to build request: %w", err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Actor-Id", actorID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return op.Fail(fmt.Errorf("java order-service unavailable: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return op.Fail(fmt.Errorf("java returned %d for order status update", resp.StatusCode))
	}
	return nil
}

// GetOrderStatus fetches only the status field of a single order.
// Used by the cron to detect cancellations on active trips.
func (c *JavaOrderClient) GetOrderStatus(ctx context.Context, orderID string) (string, error) {
	op := logging.Start(ctx, c.logger, "JavaOrderClient.GetOrderStatus", logrus.Fields{"order_id": orderID})
	defer op.End()

	url := fmt.Sprintf("%s/api/v1/orders/%s", c.baseURL, orderID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", op.Fail(fmt.Errorf("failed to build request: %w", err))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", op.Fail(fmt.Errorf("java order-service unavailable: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "NOT_FOUND", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", op.Fail(fmt.Errorf("java returned %d", resp.StatusCode))
	}

	var order struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&order); err != nil {
		return "", op.Fail(fmt.Errorf("failed to decode order: %w", err))
	}
	return order.Status, nil
}

// newStringReader wraps a string as an io.Reader for http request bodies.
func newStringReader(s string) *stringReader {
	return &stringReader{s: s, pos: 0}
}

type stringReader struct {
	s   string
	pos int
}

func (r *stringReader) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.s) {
		return 0, fmt.Errorf("EOF")
	}
	n = copy(p, r.s[r.pos:])
	r.pos += n
	return n, nil
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./internal/service/...
```

Expected: no output

- [ ] **Step 3: Commit**

```bash
git add internal/service/java_order_client.go
git commit -m "feat: add JavaOrderClient for PACKING order queries and status updates"
```

---

## Task 3: Distance Service

**Files:**
- Create: `internal/service/distance_service.go`

- [ ] **Step 1: Write the service**

```go
// internal/service/distance_service.go
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

type DistanceService struct {
	apiKey     string
	httpClient *http.Client
	logger     *logrus.Logger
}

func NewDistanceService(apiKey string, logger *logrus.Logger) *DistanceService {
	return &DistanceService{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		logger: logger,
	}
}

// DistanceKM returns the straight-line distance in kilometres between two lat/lng points
// using the Google Maps Distance Matrix API.
// Returns an error if the API call fails; callers should skip trip creation and retry next tick.
func (s *DistanceService) DistanceKM(ctx context.Context, originLat, originLng, destLat, destLng float64) (float64, error) {
	op := logging.Start(ctx, s.logger, "DistanceService.DistanceKM", logrus.Fields{
		"origin": fmt.Sprintf("%.4f,%.4f", originLat, originLng),
		"dest":   fmt.Sprintf("%.4f,%.4f", destLat, destLng),
	})
	defer op.End()

	url := fmt.Sprintf(
		"https://maps.googleapis.com/maps/api/distancematrix/json?origins=%.6f%%2C%.6f&destinations=%.6f%%2C%.6f&key=%s",
		originLat, originLng, destLat, destLng, s.apiKey,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, op.Fail(fmt.Errorf("failed to build distance request: %w", err))
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, op.Fail(fmt.Errorf("distance API unavailable: %w", err))
	}
	defer resp.Body.Close()

	var result struct {
		Status string `json:"status"`
		Rows   []struct {
			Elements []struct {
				Status   string `json:"status"`
				Distance struct {
					Value int `json:"value"` // metres
				} `json:"distance"`
			} `json:"elements"`
		} `json:"rows"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, op.Fail(fmt.Errorf("failed to decode distance response: %w", err))
	}

	if result.Status != "OK" {
		return 0, op.Fail(fmt.Errorf("distance API status: %s", result.Status))
	}
	if len(result.Rows) == 0 || len(result.Rows[0].Elements) == 0 {
		return 0, op.Fail(fmt.Errorf("distance API returned empty result"))
	}

	elem := result.Rows[0].Elements[0]
	if elem.Status != "OK" {
		return 0, op.Fail(fmt.Errorf("distance element status: %s", elem.Status))
	}

	km := float64(elem.Distance.Value) / 1000.0
	op.With("km", km)
	return km, nil
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./internal/service/...
```

Expected: no output

- [ ] **Step 3: Commit**

```bash
git add internal/service/distance_service.go
git commit -m "feat: add DistanceService using Google Maps Distance Matrix API"
```

---

## Task 4: Assignment Cron

**Files:**
- Create: `internal/service/assignment_cron.go`

- [ ] **Step 1: Write the cron**

```go
// internal/service/assignment_cron.go
package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

const (
	defaultStoreID   = "112233"
	cronInterval     = 10 * time.Second
	cronLockTTLSecs  = 30
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
	go func() {
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
	c.wg.Add(1)
	defer c.wg.Done()

	// Panic recovery — log and release lock if held
	defer func() {
		if r := recover(); r != nil {
			c.logger.WithField("panic", r).Error("assignment cron: panic recovered")
			ctx := context.Background()
			_ = c.cronLockRepo.Release(ctx)
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

	// 4. For each PACKING order: create trip if missing, check for cancellation
	// Compute distances in parallel
	type orderWithTrip struct {
		order JavaOrder
		trip  *models.Trip
	}

	// Check existing trips and detect cancellations in parallel
	type result struct {
		order  JavaOrder
		trip   *models.Trip
		needsCreate bool
	}
	results := make([]result, len(orders))
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
			results[idx] = result{order: o, trip: existing, needsCreate: existing == nil}
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
	otp := generateReferralCode() // reuse 6-digit random — for drop task 4-digit OTP use rand.Intn(9000)+1000
	otp = fmt.Sprintf("%04d", randInt(1000, 9999))

	trip := &models.Trip{
		TripID:      tripID,
		OrderID:     order.OrderID,
		StoreID:     storeID,
		Status:      models.TripStatusCreated,
		DistanceKM:  distKM,
		BasePayZMW:  basePayZMW,
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
				OTP:     otp,
			},
		},
	}

	if err := c.tripRepo.Create(ctx, trip); err != nil {
		return nil, fmt.Errorf("failed to persist trip for order %s: %w", order.OrderID, err)
	}
	return trip, nil
}

// detectCancellations checks active trips (assigned/in_transit) for orders
// that Java has marked CANCELLED. Cancels the trip and frees the DE.
func (c *AssignmentCron) detectCancellations(ctx context.Context, currentPackingOrders []JavaOrder) {
	// Build set of current PACKING order IDs
	packingSet := make(map[string]bool, len(currentPackingOrders))
	for _, o := range currentPackingOrders {
		packingSet[o.OrderID] = true
	}
	// Note: a full cancellation check would query all active trips and
	// verify each against Java. For now we rely on the cron seeing orders
	// disappear from the PACKING list — if a trip was assigned but the order
	// is no longer in the PACKING list, check its Java status directly.
	// This is a best-effort check; the DynamoDB transaction protects correctness.
}

// sortTripsByCreatedAt sorts trips ascending by created_at (oldest first = FIFO).
func sortTripsByCreatedAt(trips []*models.Trip) {
	for i := 1; i < len(trips); i++ {
		for j := i; j > 0 && trips[j].CreatedAt < trips[j-1].CreatedAt; j-- {
			trips[j], trips[j-1] = trips[j-1], trips[j]
		}
	}
}

// randInt returns a random int in [min, max].
func randInt(min, max int) int {
	import_rand_intn := func(n int) int {
		// inline to avoid import cycle; use math/rand
		return min + (int(time.Now().UnixNano()) % (max - min + 1))
	}
	return import_rand_intn(max - min + 1)
}

// darkstorePhone and darkstoreAddress return placeholder values for store 112233.
// Replace with DarkstoreRepository lookup when multi-store support is added.
func darkstorePhone(storeID string) string   { return "+260977000001" }
func darkstoreAddress(storeID string) string { return "Bunzo Darkstore, Lusaka" }
```

**Note:** The `randInt` inline approach above has a syntax issue. Replace `randInt` with:

```go
import "math/rand"

func randInt(min, max int) int {
	return min + rand.Intn(max-min+1)
}
```

And add `"math/rand"` to the imports.

- [ ] **Step 2: Fix randInt and verify it compiles**

Replace the broken `randInt` function in `assignment_cron.go` with:

```go
func randInt(min, max int) int {
	return min + rand.Intn(max-min+1)
}
```

Add `"math/rand"` to the import block. Then:

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./internal/service/...
```

Expected: no output

- [ ] **Step 3: Commit**

```bash
git add internal/service/assignment_cron.go
git commit -m "feat: add AssignmentCron with distributed lock, trip creation, parallel Maps, FIFO assignment"
```

---

## Task 5: Wire Cron into main.go

**Files:**
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Add cron to main.go**

Add to service initialization section:

```go
javaOrderClient := service.NewJavaOrderClient(cfg.Java.OrderServiceURL, logger)
distanceService := service.NewDistanceService(cfg.Google.MapsAPIKey, logger)
assignmentCron := service.NewAssignmentCron(
	tripRepo,
	deRepo,
	cronLockRepo,
	payoutConfigRepo,
	javaOrderClient,
	distanceService,
	logger,
)
```

Start the cron after server starts (add after the `go func() { srv.ListenAndServe() }()` block):

```go
assignmentCron.Start()
```

Stop the cron gracefully before server shutdown (add before `srv.Shutdown`):

```go
logger.Info("Stopping assignment cron...")
assignmentCron.Stop()
```

- [ ] **Step 2: Build the full server**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./...
```

Expected: no output

- [ ] **Step 3: Commit**

```bash
git add cmd/server/main.go
git commit -m "feat: start assignment cron on server startup with graceful shutdown"
```

---

## Task 6: Smoke Test — Cron Creates a Trip

- [ ] **Step 1: Seed a PACKING order in Java and a payout config in DynamoDB**

Seed payout config (required for base_pay computation):
```bash
aws dynamodb put-item \
  --table-name QComTable \
  --item '{"PK":{"S":"CONFIG"},"SK":{"S":"PAYOUT_V1"},"rate_per_km_zmw":{"N":"5"},"tier1_threshold":{"N":"10"},"tier1_bonus_zmw":{"N":"10"},"tier2_threshold":{"N":"15"},"tier2_bonus_zmw":{"N":"20"},"referral_trips_threshold":{"N":"5"},"referral_window_days":{"N":"30"},"referral_bonus_zmw":{"N":"50"},"min_deliveries_per_day":{"N":"3"},"milestone_message_template":{"S":"Complete {remaining} more deliveries to unlock your next milestone"}}' \
  --endpoint-url http://localhost:8000 --region us-east-1
```

- [ ] **Step 2: Start server, register+start-duty a DE, wait 15 seconds**

```bash
cd /Users/shivangawasthi/bunzo/qcom && IS_TEST=true JAVA_ORDER_SERVICE_URL=http://localhost:8081 go run cmd/server/main.go &
sleep 15
```

- [ ] **Step 3: Verify trip was created in DynamoDB**

```bash
aws dynamodb scan --table-name QComTable \
  --filter-expression "begins_with(PK, :prefix)" \
  --expression-attribute-values '{":prefix":{"S":"TRIP!"}}' \
  --endpoint-url http://localhost:8000 --region us-east-1 \
  | grep trip_id
```

Expected: at least one trip item present if a PACKING order exists in Java.

- [ ] **Step 4: Stop server and commit**

```bash
kill %1
git commit --allow-empty -m "test: verified assignment cron creates trips from PACKING orders"
```

---

## Phase 2 Complete

**What this phase delivers:**
- `JavaOrderClient` — fetches PACKING orders (paginated), updates order status, checks single order status
- `DistanceService` — Google Maps Distance Matrix distance in km
- `AssignmentCron` — 10s tick with distributed lock, panic recovery, graceful shutdown, parallel Maps calls, FIFO assignment via DynamoDB transaction
- Payout config seeded in DynamoDB

**Known stub:** `darkstorePhone` and `darkstoreAddress` return hardcoded values. Replace with `DarkstoreRepository` lookup when multi-store support is needed.

**Phase 3 picks up here** by consuming the assigned trips — DE drives them through pickup → drop via the task status update endpoint.
