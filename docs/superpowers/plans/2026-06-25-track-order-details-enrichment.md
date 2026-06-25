# Track API Order-Details Enrichment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make qcom's `GET /api/v1/orders/{orderId}/track` return the full order-details payload plus `otp`, `de_name`, and `eta`, so one call can power the customer order details screen.

**Architecture:** Pass-through enrichment. The track handler fetches the full order JSON from the Java order-service as a loosely-typed object (`map[string]json.RawMessage`), looks up the trip in DynamoDB, computes the three trip-derived fields, injects them into the order object, and returns the merged JSON. Order-service stays the single source of truth for all order fields; qcom owns only the three tracking fields. To make the handler unit-testable, its concrete dependencies are replaced with small interface seams.

**Tech Stack:** Go, gorilla/mux, logrus, AWS SDK DynamoDB (existing repositories), standard `net/http` + `net/http/httptest` for tests.

## Global Constraints

- Backend only (qcom). No BunzoApp or Java order-service changes.
- No new third-party dependencies.
- `{orderId}` path param is the human-readable order number (e.g. `ORD1162844363`); pass it verbatim to both the trip lookup and the Java client.
- ETA is a fixed 15-minute promise computed in Africa/Lusaka time via the existing `computeETA`. Do not change it.
- `trip_status` is NOT part of the response (dropped intentionally).
- OTP visibility is unchanged: only while a driver is committed (`accepted`/`out_for_delivery`) and the drop task status is `created`.
- The order payload is required — hard-fail if it can't be fetched. Trip/DE enrichment is best-effort — soft-fail to `null`.
- All added fields (`otp`, `de_name`, `eta`) are top-level keys and are JSON `null` when not applicable.

---

### Task 1: Add `GetOrderRaw` to JavaOrderClient

**Files:**
- Modify: `internal/service/java_order_client.go`
- Test: `internal/service/java_order_client_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `func (c *JavaOrderClient) GetOrderRaw(ctx context.Context, orderID string) (map[string]json.RawMessage, error)` — returns the full order object with all fields preserved verbatim; `(nil, nil)` when the order does not exist (HTTP 404); a non-nil error on transport failure or any other non-200 status.

- [ ] **Step 1: Write the failing tests**

Add to `internal/service/java_order_client_test.go`. These need `net/http/httptest`, `context`, and `github.com/sirupsen/logrus`, so update the import block accordingly (`encoding/json` and `testing` are already imported):

```go
func newTestJavaClient(serverURL string) *JavaOrderClient {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return NewJavaOrderClient(serverURL, logger)
}

func TestGetOrderRaw_ReturnsFullPayloadVerbatim(t *testing.T) {
	const body = `{"orderNumber":"ORD123","status":"OUT_FOR_DELIVERY","grandTotal":42.5,"items":[{"sku":"SKU-1","quantity":2}],"delivery":{"address":"12 Cairo Rd","phone":"0971234567"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/orders/ORD123" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
	defer srv.Close()

	got, err := newTestJavaClient(srv.URL).GetOrderRaw(context.Background(), "ORD123")
	if err != nil {
		t.Fatalf("GetOrderRaw error: %v", err)
	}
	if got == nil {
		t.Fatal("expected payload, got nil")
	}
	if string(got["orderNumber"]) != `"ORD123"` {
		t.Errorf("orderNumber not preserved: %s", got["orderNumber"])
	}
	if string(got["grandTotal"]) != `42.5` {
		t.Errorf("grandTotal not preserved verbatim: %s", got["grandTotal"])
	}
	if _, ok := got["items"]; !ok {
		t.Error("items key missing")
	}
	if _, ok := got["delivery"]; !ok {
		t.Error("delivery key missing")
	}
}

func TestGetOrderRaw_NotFoundReturnsNilNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	got, err := newTestJavaClient(srv.URL).GetOrderRaw(context.Background(), "MISSING")
	if err != nil {
		t.Fatalf("expected nil error for 404, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil map for 404, got %v", got)
	}
}

func TestGetOrderRaw_ServerErrorReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := newTestJavaClient(srv.URL).GetOrderRaw(context.Background(), "ORD123")
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
}
```

Add these imports to the test file's import block:

```go
import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirupsen/logrus"
)
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go test ./internal/service/ -run TestGetOrderRaw -v`
Expected: compile error / FAIL — `c.GetOrderRaw undefined`.

- [ ] **Step 3: Implement `GetOrderRaw`**

Add to `internal/service/java_order_client.go` (the `encoding/json`, `fmt`, `net/http`, `context` imports already exist):

```go
// GetOrderRaw fetches the full order payload as a loosely-typed object so every
// field is preserved verbatim. Returns (nil, nil) when the order does not exist.
func (c *JavaOrderClient) GetOrderRaw(ctx context.Context, orderID string) (map[string]json.RawMessage, error) {
	op := logging.Start(ctx, c.logger, "JavaOrderClient.GetOrderRaw", logrus.Fields{"order_id": orderID})
	defer op.End()

	url := fmt.Sprintf("%s/api/v1/orders/%s", c.baseURL, orderID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to build request: %w", err))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, op.Fail(fmt.Errorf("java order-service unavailable: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, op.Fail(fmt.Errorf("java returned %d", resp.StatusCode))
	}

	var order map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&order); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to decode order: %w", err))
	}
	return order, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go test ./internal/service/ -run TestGetOrderRaw -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
cd /Users/shivangawasthi/bunzo/qcom
git add internal/service/java_order_client.go internal/service/java_order_client_test.go
git commit -m "feat(track): add JavaOrderClient.GetOrderRaw for full order payload"
```

---

### Task 2: Add the pure tracking-field injection helper

**Files:**
- Modify: `internal/handlers/track_handlers.go`
- Test: `internal/handlers/track_handlers_test.go`

**Interfaces:**
- Consumes: `ETAPayload` (already defined in `track_handlers.go`).
- Produces: `func enrichOrderWithTracking(order map[string]json.RawMessage, otp *string, deName *string, eta *ETAPayload) error` — injects keys `otp`, `de_name`, `eta` into `order` in place; nil pointers serialize to JSON `null`. Returns an error only if marshalling fails.

- [ ] **Step 1: Write the failing tests**

Add to `internal/handlers/track_handlers_test.go` (add `encoding/json` to the import block):

```go
func TestEnrichOrderWithTracking_AllPresent(t *testing.T) {
	order := map[string]json.RawMessage{"orderNumber": json.RawMessage(`"ORD1"`)}
	otp := "1234"
	name := "John M."
	eta := &ETAPayload{ExpiresAt: "2026-06-25T10:00:00Z", RemainingMinutes: 8, IsDelayed: false}

	if err := enrichOrderWithTracking(order, &otp, &name, eta); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(order["otp"]) != `"1234"` {
		t.Errorf("otp = %s", order["otp"])
	}
	if string(order["de_name"]) != `"John M."` {
		t.Errorf("de_name = %s", order["de_name"])
	}
	if string(order["orderNumber"]) != `"ORD1"` {
		t.Errorf("original field clobbered: %s", order["orderNumber"])
	}
	var gotETA ETAPayload
	if err := json.Unmarshal(order["eta"], &gotETA); err != nil {
		t.Fatalf("eta not valid json: %v", err)
	}
	if gotETA.RemainingMinutes != 8 {
		t.Errorf("eta.remaining_minutes = %d", gotETA.RemainingMinutes)
	}
}

func TestEnrichOrderWithTracking_NilsBecomeJSONNull(t *testing.T) {
	order := map[string]json.RawMessage{}
	if err := enrichOrderWithTracking(order, nil, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, k := range []string{"otp", "de_name", "eta"} {
		if string(order[k]) != "null" {
			t.Errorf("%s = %s, want null", k, order[k])
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go test ./internal/handlers/ -run TestEnrichOrderWithTracking -v`
Expected: compile error — `enrichOrderWithTracking` undefined.

- [ ] **Step 3: Implement the helper**

Add to `internal/handlers/track_handlers.go` (add `encoding/json` if not already imported — it is):

```go
// enrichOrderWithTracking injects the trip-derived tracking fields into the
// order object in place. nil pointers are written as JSON null.
func enrichOrderWithTracking(order map[string]json.RawMessage, otp *string, deName *string, eta *ETAPayload) error {
	for key, val := range map[string]any{"otp": otp, "de_name": deName, "eta": eta} {
		raw, err := json.Marshal(val)
		if err != nil {
			return err
		}
		order[key] = raw
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go test ./internal/handlers/ -run TestEnrichOrderWithTracking -v`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
cd /Users/shivangawasthi/bunzo/qcom
git add internal/handlers/track_handlers.go internal/handlers/track_handlers_test.go
git commit -m "feat(track): add enrichOrderWithTracking JSON injection helper"
```

---

### Task 3: Rewrite the Track handler as pass-through enrichment

**Files:**
- Modify: `internal/handlers/track_handlers.go`
- Test: `internal/handlers/track_handlers_test.go`

**Interfaces:**
- Consumes: `JavaOrderClient.GetOrderRaw` (Task 1), `enrichOrderWithTracking` (Task 2), `computeETA` (existing), `models.Trip`/`models.DeliveryExecutive`, `models.TripStatus*`/`models.TaskStatusCreated` constants, `Trip.DropTask()`.
- Produces: enriched `200` response from `GET /api/v1/orders/{orderId}/track`. No new exported symbols other than the three interface types below.

The handler's concrete dependencies are replaced with interface seams so it can be unit-tested with fakes. The concrete `*service.JavaOrderClient`, `*repository.TripRepository`, and `*repository.DERepository` already satisfy these method sets.

- [ ] **Step 1: Add interface seams and switch struct fields (no behavior change yet)**

In `internal/handlers/track_handlers.go`, add `"context"` to the import block, then define the seams and update the struct/constructor:

```go
// Dependency seams — the concrete service/repositories satisfy these.
type trackOrderFetcher interface {
	GetOrderRaw(ctx context.Context, orderID string) (map[string]json.RawMessage, error)
}
type trackTripGetter interface {
	GetByOrderID(ctx context.Context, orderID string) (*models.Trip, error)
}
type trackDEResolver interface {
	GetByPhone(ctx context.Context, phone string) (*models.DeliveryExecutive, error)
}

type TrackHandlers struct {
	tripRepo   trackTripGetter
	deRepo     trackDEResolver
	javaClient trackOrderFetcher
	logger     *logrus.Logger
}
```

Update the constructor to keep its existing concrete parameter types (so `main.go` wiring is unchanged) while storing them as interfaces:

```go
func NewTrackHandlers(
	tripRepo *repository.TripRepository,
	deRepo *repository.DERepository,
	javaClient *service.JavaOrderClient,
	logger *logrus.Logger,
) *TrackHandlers {
	return &TrackHandlers{
		tripRepo:   tripRepo,
		deRepo:     deRepo,
		javaClient: javaClient,
		logger:     logger,
	}
}
```

- [ ] **Step 2: Write the failing handler tests**

Add to `internal/handlers/track_handlers_test.go`. Add `bytes`, `context`, `net/http`, `net/http/httptest`, `io`, `github.com/gorilla/mux`, `github.com/sirupsen/logrus`, and `github.com/qcom/qcom/internal/models` to the import block:

```go
type fakeOrderFetcher struct {
	raw map[string]json.RawMessage
	err error
}

func (f fakeOrderFetcher) GetOrderRaw(ctx context.Context, orderID string) (map[string]json.RawMessage, error) {
	return f.raw, f.err
}

type fakeTripGetter struct {
	trip *models.Trip
	err  error
}

func (f fakeTripGetter) GetByOrderID(ctx context.Context, orderID string) (*models.Trip, error) {
	return f.trip, f.err
}

type fakeDEResolver struct {
	de  *models.DeliveryExecutive
	err error
}

func (f fakeDEResolver) GetByPhone(ctx context.Context, phone string) (*models.DeliveryExecutive, error) {
	return f.de, f.err
}

func baseOrder() map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"orderNumber":  json.RawMessage(`"ORD1"`),
		"status":       json.RawMessage(`"OUT_FOR_DELIVERY"`),
		"grandTotal":   json.RawMessage(`42.5`),
		"items":        json.RawMessage(`[{"sku":"SKU-1","quantity":2}]`),
		"delivery":     json.RawMessage(`{"address":"12 Cairo Rd","phone":"0971234567"}`),
		"refundSummary": json.RawMessage(`null`),
	}
}

func tripWith(status models.TripStatus, dropStatus models.TaskStatus, otp string) *models.Trip {
	return &models.Trip{
		OrderID:   "ORD1",
		Status:    status,
		DEPhone:   "0990000000",
		CreatedAt: timezone.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339),
		Tasks: []models.Task{
			{TaskID: "pick", Type: models.TaskTypePickup, Status: models.TaskStatusCompleted},
			{TaskID: "drop", Type: models.TaskTypeDrop, Status: dropStatus, OTP: otp},
		},
	}
}

func newTrackHandlers(of trackOrderFetcher, tg trackTripGetter, de trackDEResolver) *TrackHandlers {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return &TrackHandlers{tripRepo: tg, deRepo: de, javaClient: of, logger: logger}
}

func doTrack(h *TrackHandlers, orderID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/"+orderID+"/track", nil)
	req = mux.SetURLVars(req, map[string]string{"orderId": orderID})
	rec := httptest.NewRecorder()
	h.Track(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

func TestTrack_OutForDelivery_ShowsOTPNameETAAndPreservesOrder(t *testing.T) {
	of := fakeOrderFetcher{raw: baseOrder()}
	tg := fakeTripGetter{trip: tripWith(models.TripStatusOutForDelivery, models.TaskStatusCreated, "4321")}
	de := fakeDEResolver{de: &models.DeliveryExecutive{Name: "John M."}}
	rec := doTrack(newTrackHandlers(of, tg, de), "ORD1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if string(body["otp"]) != `"4321"` {
		t.Errorf("otp = %s", body["otp"])
	}
	if string(body["de_name"]) != `"John M."` {
		t.Errorf("de_name = %s", body["de_name"])
	}
	if string(body["eta"]) == "null" || len(body["eta"]) == 0 {
		t.Errorf("eta missing: %s", body["eta"])
	}
	// order fields preserved verbatim
	if string(body["grandTotal"]) != `42.5` || string(body["status"]) != `"OUT_FOR_DELIVERY"` {
		t.Errorf("order fields not preserved: grandTotal=%s status=%s", body["grandTotal"], body["status"])
	}
}

func TestTrack_NoTrip_NullTrackingFields(t *testing.T) {
	of := fakeOrderFetcher{raw: baseOrder()}
	tg := fakeTripGetter{trip: nil}
	rec := doTrack(newTrackHandlers(of, tg, fakeDEResolver{}), "ORD1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := decodeBody(t, rec)
	for _, k := range []string{"otp", "de_name", "eta"} {
		if string(body[k]) != "null" {
			t.Errorf("%s = %s, want null", k, body[k])
		}
	}
}

func TestTrack_FindingDriver_ETAOnlyNoOTPNoName(t *testing.T) {
	of := fakeOrderFetcher{raw: baseOrder()}
	tg := fakeTripGetter{trip: tripWith(models.TripStatusAssigned, models.TaskStatusCreated, "4321")}
	rec := doTrack(newTrackHandlers(of, tg, fakeDEResolver{de: &models.DeliveryExecutive{Name: "John M."}}), "ORD1")

	body := decodeBody(t, rec)
	if string(body["otp"]) != "null" {
		t.Errorf("otp should be null before commit: %s", body["otp"])
	}
	if string(body["de_name"]) != "null" {
		t.Errorf("de_name should be null before commit: %s", body["de_name"])
	}
	if string(body["eta"]) == "null" {
		t.Error("eta should be present once trip exists")
	}
}

func TestTrack_Delivered_NoOTPNoName(t *testing.T) {
	of := fakeOrderFetcher{raw: baseOrder()}
	tg := fakeTripGetter{trip: tripWith(models.TripStatusCompleted, models.TaskStatusCompleted, "4321")}
	rec := doTrack(newTrackHandlers(of, tg, fakeDEResolver{de: &models.DeliveryExecutive{Name: "John M."}}), "ORD1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (delivered must not 400)", rec.Code)
	}
	body := decodeBody(t, rec)
	if string(body["otp"]) != "null" {
		t.Errorf("otp should be null after delivery: %s", body["otp"])
	}
	if string(body["de_name"]) != "null" {
		t.Errorf("de_name should be null after completion: %s", body["de_name"])
	}
}

func TestTrack_Cancelled_NoOTP(t *testing.T) {
	of := fakeOrderFetcher{raw: baseOrder()}
	tg := fakeTripGetter{trip: tripWith(models.TripStatusCancelled, models.TaskStatusCreated, "4321")}
	rec := doTrack(newTrackHandlers(of, tg, fakeDEResolver{}), "ORD1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (cancelled must not 400)", rec.Code)
	}
	body := decodeBody(t, rec)
	if string(body["otp"]) != "null" {
		t.Errorf("otp = %s, want null", body["otp"])
	}
}

func TestTrack_OrderNotFound_404(t *testing.T) {
	of := fakeOrderFetcher{raw: nil} // 404 maps to nil map
	tg := fakeTripGetter{trip: nil}
	rec := doTrack(newTrackHandlers(of, tg, fakeDEResolver{}), "MISSING")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestTrack_OrderServiceError_502(t *testing.T) {
	of := fakeOrderFetcher{err: errors.New("boom")}
	tg := fakeTripGetter{trip: nil}
	rec := doTrack(newTrackHandlers(of, tg, fakeDEResolver{}), "ORD1")

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestTrack_TripLookupError_DegradesTo200(t *testing.T) {
	of := fakeOrderFetcher{raw: baseOrder()}
	tg := fakeTripGetter{err: errors.New("dynamo down")}
	rec := doTrack(newTrackHandlers(of, tg, fakeDEResolver{}), "ORD1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (trip lookup is best-effort)", rec.Code)
	}
	body := decodeBody(t, rec)
	if string(body["orderNumber"]) != `"ORD1"` {
		t.Errorf("order payload should still be present: %s", body["orderNumber"])
	}
	for _, k := range []string{"otp", "de_name", "eta"} {
		if string(body[k]) != "null" {
			t.Errorf("%s = %s, want null on degraded path", k, body[k])
		}
	}
}
```

Add to the import block (alongside existing `testing`, `time`, `timezone`):

```go
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)
```

NOTE: `models.TaskTypePickup` (`"pickup"`) and `models.TaskTypeDrop` (`"drop"`) are confirmed in `internal/models/trip.go`; `Trip.DropTask()` returns the task whose `Type == TaskTypeDrop`.

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go test ./internal/handlers/ -run TestTrack_ -v`
Expected: FAIL — the current handler still 400s on completed/cancelled and returns the old 4-field shape (no order fields), so multiple assertions fail.

- [ ] **Step 4: Rewrite the `Track` handler body**

Replace the body of `func (h *TrackHandlers) Track` in `internal/handlers/track_handlers.go` with:

```go
// Track handles GET /api/v1/orders/{orderId}/track.
// Returns the full order-details payload (from order-service) enriched with the
// trip-derived fields otp, de_name, and eta. Requires customer JWT auth.
func (h *TrackHandlers) Track(w http.ResponseWriter, r *http.Request) {
	orderID := mux.Vars(r)["orderId"]
	if strings.TrimSpace(orderID) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "orderId is required")
		return
	}

	// Order payload is required — hard-fail if we can't load it.
	order, err := h.javaClient.GetOrderRaw(r.Context(), orderID)
	if err != nil {
		h.logger.WithError(err).Error("track: failed to fetch order from order-service")
		h.respondWithError(w, http.StatusBadGateway, "FETCH_FAILED", "Failed to fetch order")
		return
	}
	if order == nil {
		h.respondWithError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "Order not found")
		return
	}

	// Trip enrichment is best-effort — a lookup failure degrades to null fields.
	trip, err := h.tripRepo.GetByOrderID(r.Context(), orderID)
	if err != nil {
		h.logger.WithError(err).Error("track: trip lookup failed, returning order without tracking fields")
		trip = nil
	}

	var otp, deName *string
	var eta *ETAPayload
	if trip != nil {
		if trip.CreatedAt != "" {
			eta = computeETA(trip.CreatedAt)
		}
		// Driver and OTP are revealed only once the driver has committed
		// (accepted/out_for_delivery) — never during the pending-accept window.
		committed := trip.Status == models.TripStatusAccepted || trip.Status == models.TripStatusOutForDelivery
		if committed && trip.DEPhone != "" {
			if de, derr := h.deRepo.GetByPhone(r.Context(), trip.DEPhone); derr == nil && de != nil {
				deName = &de.Name
			}
		}
		if committed {
			if drop := trip.DropTask(); drop != nil && drop.Status == models.TaskStatusCreated {
				otp = &drop.OTP
			}
		}
	}

	if err := enrichOrderWithTracking(order, otp, deName, eta); err != nil {
		h.logger.WithError(err).Error("track: failed to enrich order payload")
		h.respondWithError(w, http.StatusInternalServerError, "ENRICH_FAILED", "Failed to build response")
		return
	}

	h.respondWithJSON(w, http.StatusOK, order)
}
```

Remove the now-unused `etaMinutes`-adjacent dead branches only if they were inside `Track` (they were). Keep `computeETA`, `ETAPayload`, and the `etaMinutes` const. The `TrackResponse` struct is no longer used by the handler — delete it to avoid an unused-type lint, unless other files reference it (grep first: `rg "TrackResponse" internal/`; if no other references, delete the struct).

- [ ] **Step 5: Run the handler tests to verify they pass**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go test ./internal/handlers/ -run 'TestTrack_|TestComputeETA|TestEnrichOrderWithTracking' -v`
Expected: PASS (all Track, ETA, and enrich tests).

- [ ] **Step 6: Build the whole module and run the full package tests**

Run: `cd /Users/shivangawasthi/bunzo/qcom && go build ./... && go test ./internal/handlers/ ./internal/service/`
Expected: build succeeds (confirms `main.go` wiring still compiles with the interface-typed struct), tests PASS.

- [ ] **Step 7: Commit**

```bash
cd /Users/shivangawasthi/bunzo/qcom
git add internal/handlers/track_handlers.go internal/handlers/track_handlers_test.go
git commit -m "feat(track): return full order details enriched with otp, de_name, eta"
```

---

## Self-Review

**1. Spec coverage:**
- JavaOrderClient full-payload fetch → Task 1. ✓
- Pass-through enrichment / inject otp,de_name,eta → Tasks 2 + 3. ✓
- Always return full payload; 404 only when order missing; removed 400/finding_driver → Task 3 Step 4 + tests. ✓
- Tracking field visibility rules (committed + drop created for otp; committed for de_name; eta when trip exists) → Task 3 Step 4 + matrix tests. ✓
- `trip_status` dropped → not added anywhere; `TrackResponse` struct removed. ✓
- Error handling: 502 order-service, 404 not found, best-effort trip/DE → Task 3 tests `OrderServiceError_502`, `OrderNotFound_404`, `TripLookupError_DegradesTo200`. ✓
- Tests for each state-matrix row → Task 3 Step 2. ✓

**2. Placeholder scan:** No TBDs; every code step has complete code. The one verification note (Task 3 Step 2) names the exact file to check and the fallback action — not a placeholder.

**3. Type consistency:** `GetOrderRaw(ctx, string) (map[string]json.RawMessage, error)` is defined in Task 1 and consumed by the `trackOrderFetcher` interface and handler in Task 3 — identical signature. `enrichOrderWithTracking(order, otp, deName, eta)` defined in Task 2, called in Task 3 with matching `*string`/`*ETAPayload` types. Interface method names (`GetByOrderID`, `GetByPhone`, `GetOrderRaw`) match the verified concrete repository/client signatures. `models.DeliveryExecutive.Name`, `Trip.DropTask()`, `models.TripStatus*`, `models.TaskStatusCreated` all confirmed against source.
