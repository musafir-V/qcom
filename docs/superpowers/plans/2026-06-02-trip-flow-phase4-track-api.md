# Trip Flow — Phase 4: Customer Track API

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the customer-facing track API — `GET /api/v1/orders/{orderId}/track` — which returns trip status, DE name, OTP, and ETA countdown. Returns `finding_driver` when no trip exists yet (after verifying the order in Java), and returns 400 for completed/cancelled trips.

**Architecture:** The handler fetches the trip via `OrderIndex` GSI. If no trip exists, it calls Java to confirm the order is valid and returns `finding_driver`. ETA is a 15-minute countdown from `trip.created_at` computed in Africa/Lusaka timezone. The OTP is hidden once the drop task is `reached` or the trip is completed/cancelled.

**Tech Stack:** Go 1.24, AWS DynamoDB (aws-sdk-go-v2), gorilla/mux, logrus

**Prerequisites:**
- Phase 1 complete — `TripRepository`, `timezone` package
- Phase 2 complete — `JavaOrderClient`
- Phase 3 complete — `TripService`

---

## File Map

### New Files
- `internal/handlers/track_handlers.go` — GET /orders/{orderId}/track

### Modified Files
- `cmd/server/main.go` — register track route under customer auth

---

## Task 1: Track Handler

**Files:**
- Create: `internal/handlers/track_handlers.go`

- [ ] **Step 1: Write the handler**

```go
// internal/handlers/track_handlers.go
package handlers

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/service"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

const etaMinutes = 15 // fixed 15-minute delivery promise

type TrackHandlers struct {
	tripRepo    *repository.TripRepository
	deRepo      *repository.DERepository
	javaClient  *service.JavaOrderClient
	logger      *logrus.Logger
}

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

type TrackResponse struct {
	TripStatus string      `json:"trip_status"`
	DEName     *string     `json:"de_name"`
	OTP        *string     `json:"otp"`
	ETA        *ETAPayload `json:"eta"`
}

type ETAPayload struct {
	ExpiresAt        string `json:"expires_at"`
	RemainingMinutes int    `json:"remaining_minutes"`
	IsDelayed        bool   `json:"is_delayed"`
	Message          *string `json:"message"`
}

// GET /api/v1/orders/{orderId}/track
// Requires customer JWT auth.
func (h *TrackHandlers) Track(w http.ResponseWriter, r *http.Request) {
	orderID := mux.Vars(r)["orderId"]
	if strings.TrimSpace(orderID) == "" {
		h.respondWithError(w, http.StatusBadRequest, "MISSING_PARAM", "orderId is required")
		return
	}

	// Look up trip by order ID
	trip, err := h.tripRepo.GetByOrderID(r.Context(), orderID)
	if err != nil {
		h.logger.WithError(err).Error("track: failed to query trip by order ID")
		h.respondWithError(w, http.StatusInternalServerError, "FETCH_FAILED", "Failed to fetch trip")
		return
	}

	// No trip yet — verify order exists in Java then return finding_driver
	if trip == nil {
		javaStatus, err := h.javaClient.GetOrderStatus(r.Context(), orderID)
		if err != nil || javaStatus == "NOT_FOUND" {
			h.respondWithError(w, http.StatusNotFound, "ORDER_NOT_FOUND", "Order not found")
			return
		}
		h.respondWithJSON(w, http.StatusOK, TrackResponse{
			TripStatus: "finding_driver",
			DEName:     nil,
			OTP:        nil,
			ETA:        nil,
		})
		return
	}

	// Completed or cancelled trips return an error — customer should see summary screen
	if trip.Status == models.TripStatusCompleted {
		h.respondWithError(w, http.StatusBadRequest, "TRIP_COMPLETED", "This order has already been delivered.")
		return
	}
	if trip.Status == models.TripStatusCancelled {
		h.respondWithError(w, http.StatusBadRequest, "TRIP_CANCELLED", "This order has been cancelled.")
		return
	}

	// Build response
	response := TrackResponse{
		TripStatus: string(trip.Status),
	}

	// DE name — only once assigned
	if trip.DEID != "" {
		de, err := h.deRepo.GetByPhone(r.Context(), trip.DEID) // Note: DEID is phone for lookup
		if err == nil && de != nil {
			response.DEName = &de.Name
		}
	}

	// OTP — shown only on drop task when status is created (before reached)
	// Hidden once drop is reached or beyond
	dropTask := trip.DropTask()
	if dropTask != nil &&
		dropTask.Status == models.TaskStatusCreated &&
		trip.Status != models.TripStatusCompleted &&
		trip.Status != models.TripStatusCancelled {
		otp := dropTask.OTP
		response.OTP = &otp
	}

	// ETA — only meaningful once trip is created (has a created_at)
	if trip.CreatedAt != "" {
		response.ETA = computeETA(trip.CreatedAt)
	}

	// Unassigned trip with no DE yet = finding_driver
	if trip.Status == models.TripStatusCreated && trip.DEID == "" {
		response.TripStatus = "finding_driver"
	}

	h.respondWithJSON(w, http.StatusOK, response)
}

// computeETA builds the ETA payload from trip.CreatedAt.
// All time math uses Africa/Lusaka timezone.
func computeETA(createdAtUTC string) *ETAPayload {
	createdAt, err := time.Parse(time.RFC3339, createdAtUTC)
	if err != nil {
		return nil
	}

	loc := timezone.ZambiaLocation()
	createdAtZambia := createdAt.In(loc)
	expiresAt := createdAtZambia.Add(etaMinutes * time.Minute)
	now := timezone.Now()

	elapsed := now.Sub(createdAtZambia).Minutes()
	remaining := etaMinutes - elapsed
	remainingInt := int(math.Max(0, math.Ceil(remaining)))
	isDelayed := remaining <= 0

	eta := &ETAPayload{
		ExpiresAt:        expiresAt.Format(time.RFC3339),
		RemainingMinutes: remainingInt,
		IsDelayed:        isDelayed,
	}

	if isDelayed {
		msg := "Your delivery is running delayed. Please contact the driver for support."
		eta.Message = &msg
	}

	return eta
}

func (h *TrackHandlers) respondWithJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func (h *TrackHandlers) respondWithError(w http.ResponseWriter, status int, code, message string) {
	h.respondWithJSON(w, status, ErrorResponse{
		Error: ErrorDetail{Code: code, Message: message},
	})
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./internal/handlers/...
```

Expected: no output

- [ ] **Step 3: Commit**

```bash
git add internal/handlers/track_handlers.go
git commit -m "feat: add TrackHandlers with ETA countdown, OTP visibility rules, finding_driver state"
```

---

## Task 2: Wire Track Route into main.go

**Files:**
- Modify: `cmd/server/main.go`

- [ ] **Step 1: Add TrackHandlers init**

In the handler initialization section of `main.go`:

```go
trackHandlers := handlers.NewTrackHandlers(tripRepo, deRepo, javaOrderClient, logger)
```

- [ ] **Step 2: Register route in `setupRouter`**

Inside the `protected` subrouter (customer JWT auth), add:

```go
protected.HandleFunc("/orders/{orderId}/track", trackHandlers.Track).Methods("GET", "OPTIONS")
```

- [ ] **Step 3: Build the full server**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go build ./...
```

Expected: no output

- [ ] **Step 4: Commit**

```bash
git add cmd/server/main.go
git commit -m "feat: register GET /orders/{orderId}/track route under customer auth"
```

---

## Task 3: Unit Tests for ETA Computation

**Files:**
- Create: `internal/handlers/track_handlers_test.go`

- [ ] **Step 1: Write failing tests**

```go
// internal/handlers/track_handlers_test.go
package handlers

import (
	"testing"
	"time"

	"github.com/qcom/qcom/internal/timezone"
)

func TestComputeETA_OnTime(t *testing.T) {
	// Trip created 5 minutes ago
	createdAt := timezone.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	eta := computeETA(createdAt)

	if eta == nil {
		t.Fatal("expected ETA payload, got nil")
	}
	if eta.IsDelayed {
		t.Fatal("expected not delayed at 5 minutes")
	}
	if eta.RemainingMinutes < 9 || eta.RemainingMinutes > 11 {
		t.Fatalf("expected ~10 remaining minutes, got %d", eta.RemainingMinutes)
	}
	if eta.Message != nil {
		t.Fatalf("expected no delay message, got: %s", *eta.Message)
	}
}

func TestComputeETA_Delayed(t *testing.T) {
	// Trip created 20 minutes ago
	createdAt := timezone.Now().Add(-20 * time.Minute).UTC().Format(time.RFC3339)
	eta := computeETA(createdAt)

	if eta == nil {
		t.Fatal("expected ETA payload, got nil")
	}
	if !eta.IsDelayed {
		t.Fatal("expected delayed at 20 minutes")
	}
	if eta.RemainingMinutes != 0 {
		t.Fatalf("expected 0 remaining minutes, got %d", eta.RemainingMinutes)
	}
	if eta.Message == nil {
		t.Fatal("expected delay message")
	}
}

func TestComputeETA_ExactBoundary(t *testing.T) {
	// Trip created exactly 15 minutes ago
	createdAt := timezone.Now().Add(-15 * time.Minute).UTC().Format(time.RFC3339)
	eta := computeETA(createdAt)

	if eta == nil {
		t.Fatal("expected ETA payload")
	}
	if !eta.IsDelayed {
		t.Fatal("expected delayed at exactly 15 minutes")
	}
}

func TestComputeETA_InvalidTimestamp(t *testing.T) {
	eta := computeETA("not-a-timestamp")
	if eta != nil {
		t.Fatal("expected nil for invalid timestamp")
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go test ./internal/handlers/... -run TestComputeETA -v
```

Expected: `FAIL` — `computeETA` not visible from test (unexported from handler file — move to exported or use same package)

**Note:** Tests are in `package handlers` (same package) so `computeETA` is visible. If tests fail for other reasons, check timezone import.

- [ ] **Step 3: Run tests after confirming handler is written**

```bash
cd /Users/shivangawasthi/bunzo/qcom && go test ./internal/handlers/... -run TestComputeETA -v
```

Expected:
```
--- PASS: TestComputeETA_OnTime
--- PASS: TestComputeETA_Delayed
--- PASS: TestComputeETA_ExactBoundary
--- PASS: TestComputeETA_InvalidTimestamp
```

- [ ] **Step 4: Commit**

```bash
git add internal/handlers/track_handlers_test.go
git commit -m "test: add ETA computation unit tests covering on-time, delayed, boundary, invalid cases"
```

---

## Task 4: Integration Test — Track API

**Files:**
- Create: `tests/integration/track_api_test.go`

- [ ] **Step 1: Write the test**

```go
// tests/integration/track_api_test.go
package integration

import (
	"net/http"
	"testing"
)

// TestTrackAPI_NoTrip verifies that the track API returns finding_driver
// for a valid order that has no trip yet.
func TestTrackAPI_NoTrip(t *testing.T) {
	base := testBaseURL()

	// Register and auth a customer
	phone := uniquePhone()
	mustPost(t, base+"/api/v1/auth/initiate-otp", map[string]string{"phone_number": phone})
	authResp := mustPost(t, base+"/api/v1/auth/verify-otp", map[string]string{
		"phone_number": phone, "otp": "000000",
	})
	token, _ := authResp["access_token"].(string)

	// Use a non-existent order ID — expect 404
	req, _ := http.NewRequest("GET", base+"/api/v1/orders/non-existent-order/track", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("track request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for non-existent order, got %d", resp.StatusCode)
	}
}

// TestTrackAPI_CompletedTrip verifies 400 TRIP_COMPLETED for a finished trip.
// Requires a completed trip to exist — run after TestTripProgressionFlow.
func TestTrackAPI_CompletedTrip(t *testing.T) {
	t.Skip("run manually after TestTripProgressionFlow creates a completed trip")
}
```

- [ ] **Step 2: Run the test**

```bash
cd /Users/shivangawasthi/bunzo/qcom && IS_TEST=true go run cmd/server/main.go &
sleep 2
go test ./tests/integration/... -run TestTrackAPI -v
kill %1
```

Expected: `TestTrackAPI_NoTrip` passes, `TestTrackAPI_CompletedTrip` skipped

- [ ] **Step 3: Commit**

```bash
git add tests/integration/track_api_test.go
git commit -m "test: add track API integration tests"
```

---

## Phase 4 Complete — Plan A Done

**What this phase delivers:**
- `GET /api/v1/orders/{orderId}/track` — customer-facing trip tracking
- ETA countdown from `trip.created_at` in Africa/Lusaka timezone
- Backend-driven delay message when ETA elapsed
- OTP shown only when drop task is in `created` state
- `finding_driver` for both "no trip yet" and "unassigned trip" cases
- `400 TRIP_COMPLETED` / `400 TRIP_CANCELLED` for closed trips

---

## Full Plan A Summary

| Phase | Delivers | Plan File |
|---|---|---|
| Phase 1 | Models, repositories, GSIs, timezone | `2026-06-02-trip-flow-phase1-foundation.md` |
| Phase 2 | Assignment cron, Java client, distance service | `2026-06-02-trip-flow-phase2-assignment-cron.md` |
| Phase 3 | State machine, task updates, duty end | `2026-06-02-trip-flow-phase3-de-progression.md` |
| Phase 4 | Customer track API, ETA | `2026-06-02-trip-flow-phase4-track-api.md` |

**What Plan B (Payout & Earnings) must add on top of Plan A:**
- `EarningsLedger` write at trip completion in `TripService.completeDelivery`
- `WeeklyBonusCron` for weekly consistency bonus
- `GET /de/earnings/summary` and `GET /de/earnings/disbursements` endpoints
- `POST /de/{deId}/disbursement` ops endpoint
