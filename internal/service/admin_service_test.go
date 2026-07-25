package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

type fakeReassignTripRepo struct {
	trip *models.Trip

	reassignCalled bool
	gotEntry       models.TripReassignment
	gotPromote     bool
	// All seven positional string args are captured: they are same-typed and
	// adjacent, so a transposition is invisible unless each is asserted.
	gotTripID      string
	gotFromDEPhone string
	gotFromDEID    string
	gotToDEID      string
	gotToDEPhone   string
	gotOrderID     string
	gotStoreID     string
	reassignErr    error
}

// fakeStatusEventAppender captures presence events written after a reassignment.
type fakeStatusEventAppender struct {
	events []*models.DEStatusEvent
}

func (f *fakeStatusEventAppender) Append(ctx context.Context, event *models.DEStatusEvent) error {
	f.events = append(f.events, event)
	return nil
}

func (f *fakeReassignTripRepo) GetByID(ctx context.Context, tripID string) (*models.Trip, error) {
	return f.trip, nil
}
func (f *fakeReassignTripRepo) GetByOrderID(ctx context.Context, orderID string) (*models.Trip, error) {
	return f.trip, nil
}
func (f *fakeReassignTripRepo) AdminAssign(ctx context.Context, tripID, orderID, deID, dePhone, storeID string) error {
	return nil
}
func (f *fakeReassignTripRepo) Reassign(
	ctx context.Context,
	tripID, fromDEPhone, fromDEID, toDEID, toDEPhone, orderID, storeID string,
	promoteToAccepted bool,
	entry models.TripReassignment,
) error {
	f.reassignCalled = true
	f.gotEntry = entry
	f.gotPromote = promoteToAccepted
	f.gotTripID = tripID
	f.gotFromDEPhone = fromDEPhone
	f.gotFromDEID = fromDEID
	f.gotToDEID = toDEID
	f.gotToDEPhone = toDEPhone
	f.gotOrderID = orderID
	f.gotStoreID = storeID
	return f.reassignErr
}

type fakeReassignDERepo struct {
	byPhone map[string]*models.DeliveryExecutive
	onDuty  []*models.DeliveryExecutive
}

func (f *fakeReassignDERepo) GetByPhone(ctx context.Context, phone string) (*models.DeliveryExecutive, error) {
	return f.byPhone[phone], nil
}
func (f *fakeReassignDERepo) FindOnDutyByStore(ctx context.Context, storeID string) ([]*models.DeliveryExecutive, error) {
	return f.onDuty, nil
}

func newTestAdminService(tripRepo *fakeReassignTripRepo, deRepo *fakeReassignDERepo) *AdminService {
	logger := logrus.New()
	logger.SetLevel(logrus.PanicLevel)
	return &AdminService{tripRepo: tripRepo, deRepo: deRepo, logger: logger}
}

func inFlightTrip(status models.TripStatus) *models.Trip {
	return &models.Trip{
		TripID: "T1", OrderID: "ORD-1", StoreID: "221",
		Status: status, DEID: "DE-A", DEPhone: "+260A",
		BasePayZMW: 25, SLAMinutes: 30,
		Tasks: []models.Task{
			{TaskID: "TK1", Type: models.TaskTypePickup, Status: models.TaskStatusCompleted, CompletedAt: "2026-07-25T10:00:00Z"},
			{TaskID: "TK2", Type: models.TaskTypeDrop, Status: models.TaskStatusCreated, OTP: "1234"},
		},
	}
}

func riderB(status models.DEStatus) *models.DeliveryExecutive {
	return &models.DeliveryExecutive{
		DEID: "DE-B", PhoneNumber: "+260B", Name: "Bwalya",
		Status: status, CurrentStoreID: "221",
	}
}

func repoWithB(trip *models.Trip, b *models.DeliveryExecutive) (*fakeReassignTripRepo, *fakeReassignDERepo) {
	return &fakeReassignTripRepo{trip: trip},
		&fakeReassignDERepo{byPhone: map[string]*models.DeliveryExecutive{
			"+260B": b,
			"+260A": {DEID: "DE-A", PhoneNumber: "+260A", Name: "Aaron", Status: models.DEStatusBusy},
		}}
}

func TestReassignTrip_OutForDelivery_Succeeds(t *testing.T) {
	tr, de := repoWithB(inFlightTrip(models.TripStatusOutForDelivery), riderB(models.DEStatusFree))
	svc := newTestAdminService(tr, de)

	err := svc.ReassignTrip(context.Background(), "T1", "+260B", "bike_breakdown", "chain snapped", "ops_jane")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if !tr.reassignCalled {
		t.Fatal("expected repo.Reassign to be called")
	}
	if tr.gotPromote {
		t.Fatal("out_for_delivery must not be promoted to accepted")
	}
	if tr.gotEntry.TripStatusAtReassign != models.TripStatusOutForDelivery {
		t.Fatalf("entry must record pre-reassign status, got %q", tr.gotEntry.TripStatusAtReassign)
	}
	if tr.gotEntry.FromDEID != "DE-A" || tr.gotEntry.ToDEID != "DE-B" {
		t.Fatalf("entry riders wrong: %+v", tr.gotEntry)
	}
	if tr.gotEntry.AdminUsername != "ops_jane" || tr.gotEntry.ReasonCode != "bike_breakdown" || tr.gotEntry.Note != "chain snapped" {
		t.Fatalf("entry audit fields wrong: %+v", tr.gotEntry)
	}
	if tr.gotEntry.At == "" {
		t.Fatal("entry must be timestamped")
	}

	// Reassign takes seven consecutive same-typed strings; assert every one so a
	// transposition (e.g. de_phone/de_id) fails here rather than against the
	// DynamoDB condition expression at runtime.
	for _, c := range []struct {
		name, got, want string
	}{
		{"tripID", tr.gotTripID, "T1"},
		{"fromDEPhone", tr.gotFromDEPhone, "+260A"},
		{"fromDEID", tr.gotFromDEID, "DE-A"},
		{"toDEID", tr.gotToDEID, "DE-B"},
		{"toDEPhone", tr.gotToDEPhone, "+260B"},
		{"orderID", tr.gotOrderID, "ORD-1"},
		{"storeID", tr.gotStoreID, "221"},
	} {
		if c.got != c.want {
			t.Errorf("Reassign arg %s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// DEStatusEvent.TS is embedded in the sort key and ListEventsForDay range-scans
// with UTC day bounds, so a local-offset stamp would sort outside its own day.
// The audit entry stays in Zambia local time; only the event TS is UTC.
func TestReassignTrip_StatusEventsAreUTC(t *testing.T) {
	tr, de := repoWithB(inFlightTrip(models.TripStatusOutForDelivery), riderB(models.DEStatusFree))
	events := &fakeStatusEventAppender{}
	svc := newTestAdminService(tr, de)
	svc.statusEventRepo = events

	if err := svc.ReassignTrip(context.Background(), "T1", "+260B", "other", "", "ops_jane"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(events.events) != 2 {
		t.Fatalf("expected 2 presence events, got %d", len(events.events))
	}
	entryAt, err := time.Parse(time.RFC3339, tr.gotEntry.At)
	if err != nil {
		t.Fatalf("entry.At is not RFC3339: %v", err)
	}
	for _, ev := range events.events {
		if !strings.HasSuffix(ev.TS, "Z") {
			t.Errorf("event TS %q for %s must be UTC (Z-suffixed)", ev.TS, ev.Phone)
		}
		ts, err := time.Parse(time.RFC3339, ev.TS)
		if err != nil {
			t.Fatalf("event TS is not RFC3339: %v", err)
		}
		if !ts.Equal(entryAt) {
			t.Errorf("event TS %q and entry.At %q must be the same instant", ev.TS, tr.gotEntry.At)
		}
		if ev.Reason != models.ReasonReassigned {
			t.Errorf("event reason = %q, want %q", ev.Reason, models.ReasonReassigned)
		}
	}
}

func TestReassignTrip_Assigned_PromotesToAccepted(t *testing.T) {
	tr, de := repoWithB(inFlightTrip(models.TripStatusAssigned), riderB(models.DEStatusEligible))
	svc := newTestAdminService(tr, de)

	if err := svc.ReassignTrip(context.Background(), "T1", "+260B", "other", "", "ops_jane"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if !tr.gotPromote {
		t.Fatal("assigned must be promoted to accepted")
	}
}

func TestReassignTrip_AcceptedNotPromoted(t *testing.T) {
	tr, de := repoWithB(inFlightTrip(models.TripStatusAccepted), riderB(models.DEStatusFree))
	svc := newTestAdminService(tr, de)

	if err := svc.ReassignTrip(context.Background(), "T1", "+260B", "other", "", "ops_jane"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if tr.gotPromote {
		t.Fatal("accepted must keep its status")
	}
}

func TestReassignTrip_RejectsNonReassignableStatuses(t *testing.T) {
	for _, status := range []models.TripStatus{
		models.TripStatusCreated, models.TripStatusCompleted,
		models.TripStatusCancelled, models.TripStatusDistanceFailed,
	} {
		tr, de := repoWithB(inFlightTrip(status), riderB(models.DEStatusFree))
		svc := newTestAdminService(tr, de)

		err := svc.ReassignTrip(context.Background(), "T1", "+260B", "other", "", "ops_jane")
		if !errors.Is(err, ErrTripNotReassignable) {
			t.Fatalf("status %s: expected ErrTripNotReassignable, got %v", status, err)
		}
		if tr.reassignCalled {
			t.Fatalf("status %s: must not call repo", status)
		}
	}
}

// DynamoDB rejects a transaction that touches the same item twice with
// ValidationException, so this must be caught before any repository call.
func TestReassignTrip_SameDriver_RejectedBeforeRepo(t *testing.T) {
	trip := inFlightTrip(models.TripStatusOutForDelivery)
	tr := &fakeReassignTripRepo{trip: trip}
	de := &fakeReassignDERepo{byPhone: map[string]*models.DeliveryExecutive{
		"+260A": {DEID: "DE-A", PhoneNumber: "+260A", Status: models.DEStatusBusy, CurrentStoreID: "221"},
	}}
	svc := newTestAdminService(tr, de)

	err := svc.ReassignTrip(context.Background(), "T1", "+260A", "other", "", "ops_jane")
	if !errors.Is(err, ErrSameDriver) {
		t.Fatalf("expected ErrSameDriver, got %v", err)
	}
	if tr.reassignCalled {
		t.Fatal("must not reach the repository")
	}
}

// DE items are keyed by phone, so a phone collision is what actually breaks the
// transaction — even if the de_ids differ (a trip row whose de_id/de_phone pair
// disagrees with the DE record). Must fail closed.
func TestReassignTrip_SamePhoneDifferentDEID_RejectedBeforeRepo(t *testing.T) {
	trip := inFlightTrip(models.TripStatusOutForDelivery)
	tr := &fakeReassignTripRepo{trip: trip}
	de := &fakeReassignDERepo{byPhone: map[string]*models.DeliveryExecutive{
		// Same phone as the trip's current rider, but a different de_id.
		"+260A": {DEID: "DE-STALE", PhoneNumber: "+260A", Status: models.DEStatusFree, CurrentStoreID: "221"},
	}}
	svc := newTestAdminService(tr, de)

	err := svc.ReassignTrip(context.Background(), "T1", "+260A", "other", "", "ops_jane")
	if !errors.Is(err, ErrSameDriver) {
		t.Fatalf("expected ErrSameDriver, got %v", err)
	}
	if tr.reassignCalled {
		t.Fatal("must not reach the repository — DynamoDB would raise ValidationException")
	}
}

func TestReassignTrip_RejectsInvalidReasonCode(t *testing.T) {
	tr, de := repoWithB(inFlightTrip(models.TripStatusAccepted), riderB(models.DEStatusFree))
	svc := newTestAdminService(tr, de)

	err := svc.ReassignTrip(context.Background(), "T1", "+260B", "bogus_reason", "", "ops_jane")
	if !errors.Is(err, ErrInvalidReasonCode) {
		t.Fatalf("expected ErrInvalidReasonCode, got %v", err)
	}
	if tr.reassignCalled {
		t.Fatal("must not reach the repository")
	}
}

func TestReassignTrip_RejectsBusyOrOfflineTarget(t *testing.T) {
	for _, status := range []models.DEStatus{models.DEStatusBusy, models.DEStatusOffline} {
		tr, de := repoWithB(inFlightTrip(models.TripStatusAccepted), riderB(status))
		svc := newTestAdminService(tr, de)

		err := svc.ReassignTrip(context.Background(), "T1", "+260B", "other", "", "ops_jane")
		if !errors.Is(err, ErrDENotEligible) {
			t.Fatalf("status %s: expected ErrDENotEligible, got %v", status, err)
		}
	}
}

func TestReassignTrip_RejectsWrongStore(t *testing.T) {
	b := riderB(models.DEStatusFree)
	b.CurrentStoreID = "999"
	tr, de := repoWithB(inFlightTrip(models.TripStatusAccepted), b)
	svc := newTestAdminService(tr, de)

	err := svc.ReassignTrip(context.Background(), "T1", "+260B", "other", "", "ops_jane")
	if !errors.Is(err, ErrDriverWrongStore) {
		t.Fatalf("expected ErrDriverWrongStore, got %v", err)
	}
}

func TestReassignTrip_MissingTargetDriver(t *testing.T) {
	tr := &fakeReassignTripRepo{trip: inFlightTrip(models.TripStatusAccepted)}
	de := &fakeReassignDERepo{byPhone: map[string]*models.DeliveryExecutive{}}
	svc := newTestAdminService(tr, de)

	err := svc.ReassignTrip(context.Background(), "T1", "+260Z", "other", "", "ops_jane")
	if !errors.Is(err, ErrDENotFound) {
		t.Fatalf("expected ErrDENotFound, got %v", err)
	}
}

func TestReassignTrip_TripNotFound(t *testing.T) {
	tr := &fakeReassignTripRepo{trip: nil}
	de := &fakeReassignDERepo{}
	svc := newTestAdminService(tr, de)

	err := svc.ReassignTrip(context.Background(), "T404", "+260B", "other", "", "ops_jane")
	if !errors.Is(err, ErrTripNotFound) {
		t.Fatalf("expected ErrTripNotFound, got %v", err)
	}
}

// A lost race on the repository transaction (e.g. the assignment cron won
// first) must surface as the service-level ErrReassignConflict so the handler
// classifies it as 409 REASSIGN_CONFLICT, not a generic 500. This is the
// error-table requirement from the spec: transaction conflict -> 409
// REASSIGN_CONFLICT.
func TestReassignTrip_RepoConflict_PropagatesAsReassignConflict(t *testing.T) {
	tr, de := repoWithB(inFlightTrip(models.TripStatusOutForDelivery), riderB(models.DEStatusFree))
	tr.reassignErr = fmt.Errorf("%w: trip moved, outgoing rider not busy on it, or incoming rider unavailable",
		repository.ErrReassignConflict)
	svc := newTestAdminService(tr, de)

	err := svc.ReassignTrip(context.Background(), "T1", "+260B", "bike_breakdown", "", "ops_jane")
	if !tr.reassignCalled {
		t.Fatal("expected repo.Reassign to be called")
	}
	if !errors.Is(err, ErrReassignConflict) {
		t.Fatalf("expected errors.Is(err, ErrReassignConflict) to hold, got %v", err)
	}
}

// The cash cap is display-only for reassignment: a customer is already waiting,
// so an over-cap rider must still be selectable.
func TestReassignTrip_OverCashLimitTargetAllowed(t *testing.T) {
	b := riderB(models.DEStatusFree)
	b.InHandCashZMW = 100000
	tr, de := repoWithB(inFlightTrip(models.TripStatusOutForDelivery), b)
	svc := newTestAdminService(tr, de)

	if err := svc.ReassignTrip(context.Background(), "T1", "+260B", "cash_limit_reached", "", "ops_jane"); err != nil {
		t.Fatalf("cash cap must not block reassignment, got %v", err)
	}
}

// Reassigning back to a previous holder is the undo path and must be allowed.
func TestReassignTrip_PreviousHolderAllowed(t *testing.T) {
	trip := inFlightTrip(models.TripStatusAccepted)
	trip.RejectedDEIDs = []string{"DE-B"}
	trip.Reassignments = []models.TripReassignment{{FromDEID: "DE-B", ToDEID: "DE-A"}}
	tr, de := repoWithB(trip, riderB(models.DEStatusFree))
	svc := newTestAdminService(tr, de)

	if err := svc.ReassignTrip(context.Background(), "T1", "+260B", "other", "", "ops_jane"); err != nil {
		t.Fatalf("previous holder must be reassignable, got %v", err)
	}
}

func TestReassignCandidates_FiltersAndFlags(t *testing.T) {
	trip := inFlightTrip(models.TripStatusOutForDelivery)
	trip.Reassignments = []models.TripReassignment{{FromDEID: "DE-P", ToDEID: "DE-A"}}
	tr := &fakeReassignTripRepo{trip: trip}
	de := &fakeReassignDERepo{onDuty: []*models.DeliveryExecutive{
		{DEID: "DE-A", PhoneNumber: "+260A", Name: "Aaron", Status: models.DEStatusBusy},
		{DEID: "DE-B", PhoneNumber: "+260B", Name: "Bwalya", Status: models.DEStatusFree, InHandCashZMW: 100},
		{DEID: "DE-C", PhoneNumber: "+260C", Name: "Chanda", Status: models.DEStatusEligible, InHandCashZMW: 900},
		{DEID: "DE-P", PhoneNumber: "+260P", Name: "Prev", Status: models.DEStatusEligible},
		{DEID: "DE-O", PhoneNumber: "+260O", Name: "Offy", Status: models.DEStatusOffline},
		// Current holder's phone under a stale de_id: ReassignTrip would reject
		// this with ErrSameDriver, so the list must not offer it either.
		{DEID: "DE-STALE", PhoneNumber: "+260A", Name: "Stale", Status: models.DEStatusFree},
	}}
	svc := newTestAdminService(tr, de)

	got, err := svc.ReassignCandidates(context.Background(), "T1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	byID := map[string]ReassignCandidate{}
	for _, c := range got {
		byID[c.DEID] = c
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 candidates (B, C, P), got %d: %+v", len(got), got)
	}
	if _, ok := byID["DE-A"]; ok {
		t.Fatal("current holder must be excluded")
	}
	if _, ok := byID["DE-O"]; ok {
		t.Fatal("offline rider must be excluded")
	}
	if _, ok := byID["DE-STALE"]; ok {
		t.Fatal("rider sharing the current holder's phone must be excluded")
	}
	if !byID["DE-C"].CashOverLimit {
		t.Fatal("DE-C is over the 500 cap and must be flagged")
	}
	if byID["DE-B"].CashOverLimit {
		t.Fatal("DE-B is under the cap and must not be flagged")
	}
	if !byID["DE-P"].PreviouslyHeld {
		t.Fatal("DE-P previously held this trip and must be flagged")
	}
	if byID["DE-B"].PreviouslyHeld {
		t.Fatal("DE-B never held this trip")
	}
}

// Only the UI must not be the sole guard against listing candidates for a
// trip that can no longer be moved — completed/cancelled trips must be
// rejected server-side too.
func TestReassignCandidates_RejectsNonReassignableStatus(t *testing.T) {
	for _, status := range []models.TripStatus{models.TripStatusCompleted, models.TripStatusCancelled} {
		trip := inFlightTrip(status)
		tr := &fakeReassignTripRepo{trip: trip}
		de := &fakeReassignDERepo{onDuty: []*models.DeliveryExecutive{
			{DEID: "DE-B", PhoneNumber: "+260B", Name: "Bwalya", Status: models.DEStatusFree},
		}}
		svc := newTestAdminService(tr, de)

		got, err := svc.ReassignCandidates(context.Background(), "T1")
		if !errors.Is(err, ErrTripNotReassignable) {
			t.Fatalf("status %s: expected ErrTripNotReassignable, got %v", status, err)
		}
		if got != nil {
			t.Fatalf("status %s: expected nil candidates, got %+v", status, got)
		}
	}
}
