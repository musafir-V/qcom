package service

import (
	"context"
	"errors"
	"testing"

	"github.com/qcom/qcom/internal/models"
)

type fakeAdminDisputeStore struct {
	getByID    *models.Dispute
	counts     map[models.DisputeStatus]int
	listResult []models.Dispute
	listCursor string
	updateOut  *models.Dispute
	gotID      string
	gotStatus  models.DisputeStatus
	gotNote    string
	gotActor   string
}

func (f *fakeAdminDisputeStore) GetByID(ctx context.Context, id string) (*models.Dispute, error) {
	return f.getByID, nil
}
func (f *fakeAdminDisputeStore) ListByStatus(ctx context.Context, status models.DisputeStatus, cursor string, limit int32) ([]models.Dispute, string, error) {
	return f.listResult, f.listCursor, nil
}
func (f *fakeAdminDisputeStore) CountByStatus(ctx context.Context, status models.DisputeStatus) (int, error) {
	return f.counts[status], nil
}
func (f *fakeAdminDisputeStore) UpdateStatus(ctx context.Context, id string, newStatus models.DisputeStatus, note, actor, now string) (*models.Dispute, error) {
	f.gotID, f.gotStatus, f.gotNote, f.gotActor = id, newStatus, note, actor
	return f.updateOut, nil
}

type fakeDispositionLookup struct{ titles map[string]string }

func (f *fakeDispositionLookup) GetByCode(ctx context.Context, code string) (*models.DisputeDisposition, error) {
	t, ok := f.titles[code]
	if !ok {
		return nil, nil
	}
	return &models.DisputeDisposition{Code: code, Title: t}, nil
}

func TestAdminUpdateStatus_HappyResolve(t *testing.T) {
	store := &fakeAdminDisputeStore{
		getByID:   &models.Dispute{DisputeID: "d1", Status: models.DisputeStatusOpen},
		updateOut: &models.Dispute{DisputeID: "d1", Status: models.DisputeStatusResolved},
	}
	svc := NewAdminDisputeService(store, &fakeDispositionLookup{}, nil, nil)
	got, err := svc.UpdateStatus(context.Background(), "d1", models.DisputeStatusResolved, "refunded customer", "alice")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Status != models.DisputeStatusResolved {
		t.Errorf("status = %v", got.Status)
	}
	if store.gotNote != "refunded customer" || store.gotActor != "alice" || store.gotID != "d1" {
		t.Errorf("repo got id=%q note=%q actor=%q", store.gotID, store.gotNote, store.gotActor)
	}
}

func TestAdminUpdateStatus_UnderReviewNoNoteOK(t *testing.T) {
	store := &fakeAdminDisputeStore{
		getByID:   &models.Dispute{DisputeID: "d1", Status: models.DisputeStatusOpen},
		updateOut: &models.Dispute{DisputeID: "d1", Status: models.DisputeStatusUnderReview},
	}
	svc := NewAdminDisputeService(store, &fakeDispositionLookup{}, nil, nil)
	if _, err := svc.UpdateStatus(context.Background(), "d1", models.DisputeStatusUnderReview, "", "alice"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestAdminUpdateStatus_NoteRequiredForResolve(t *testing.T) {
	store := &fakeAdminDisputeStore{getByID: &models.Dispute{Status: models.DisputeStatusOpen}}
	svc := NewAdminDisputeService(store, &fakeDispositionLookup{}, nil, nil)
	if _, err := svc.UpdateStatus(context.Background(), "d1", models.DisputeStatusRejected, "  ", "alice"); !errors.Is(err, ErrResolutionNoteRequired) {
		t.Fatalf("want ErrResolutionNoteRequired, got %v", err)
	}
}

func TestAdminUpdateStatus_InvalidTransition(t *testing.T) {
	store := &fakeAdminDisputeStore{getByID: &models.Dispute{Status: models.DisputeStatusResolved}}
	svc := NewAdminDisputeService(store, &fakeDispositionLookup{}, nil, nil)
	if _, err := svc.UpdateStatus(context.Background(), "d1", models.DisputeStatusOpen, "x", "alice"); !errors.Is(err, ErrInvalidDisputeTransition) {
		t.Fatalf("want ErrInvalidDisputeTransition, got %v", err)
	}
}

func TestAdminUpdateStatus_NotFound(t *testing.T) {
	store := &fakeAdminDisputeStore{getByID: nil}
	svc := NewAdminDisputeService(store, &fakeDispositionLookup{}, nil, nil)
	if _, err := svc.UpdateStatus(context.Background(), "d1", models.DisputeStatusResolved, "x", "alice"); !errors.Is(err, ErrDisputeNotFound) {
		t.Fatalf("want ErrDisputeNotFound, got %v", err)
	}
}

func TestAdminUpdateStatus_InvalidStatus(t *testing.T) {
	svc := NewAdminDisputeService(&fakeAdminDisputeStore{}, &fakeDispositionLookup{}, nil, nil)
	if _, err := svc.UpdateStatus(context.Background(), "d1", models.DisputeStatus("BOGUS"), "x", "alice"); !errors.Is(err, ErrInvalidDisputeStatus) {
		t.Fatalf("want ErrInvalidDisputeStatus, got %v", err)
	}
}

func TestAdminListByStatus_InvalidStatus(t *testing.T) {
	svc := NewAdminDisputeService(&fakeAdminDisputeStore{}, &fakeDispositionLookup{}, nil, nil)
	if _, _, err := svc.ListByStatus(context.Background(), models.DisputeStatus("NOPE"), "", 50); !errors.Is(err, ErrInvalidDisputeStatus) {
		t.Fatalf("want ErrInvalidDisputeStatus, got %v", err)
	}
}

func TestAdminSummary(t *testing.T) {
	store := &fakeAdminDisputeStore{counts: map[models.DisputeStatus]int{
		models.DisputeStatusOpen: 3, models.DisputeStatusUnderReview: 1,
		models.DisputeStatusResolved: 7, models.DisputeStatusRejected: 2,
	}}
	svc := NewAdminDisputeService(store, &fakeDispositionLookup{}, nil, nil)
	got, err := svc.Summary(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Open != 3 || got.UnderReview != 1 || got.Resolved != 7 || got.Rejected != 2 {
		t.Errorf("summary = %+v", got)
	}
}

// --- GetDetail stubs and tests ---

type adminDetailDisputeStore struct {
	byID map[string]*models.Dispute
}

func (s *adminDetailDisputeStore) GetByID(ctx context.Context, id string) (*models.Dispute, error) {
	if s.byID == nil {
		return nil, nil
	}
	return s.byID[id], nil
}
func (s *adminDetailDisputeStore) ListByStatus(ctx context.Context, status models.DisputeStatus, cursor string, limit int32) ([]models.Dispute, string, error) {
	return nil, "", nil
}
func (s *adminDetailDisputeStore) CountByStatus(ctx context.Context, status models.DisputeStatus) (int, error) {
	return 0, nil
}
func (s *adminDetailDisputeStore) UpdateStatus(ctx context.Context, id string, newStatus models.DisputeStatus, note, actor, now string) (*models.Dispute, error) {
	return nil, nil
}

type adminDetailDispositionStore struct{}

func (s *adminDetailDispositionStore) GetByCode(ctx context.Context, code string) (*models.DisputeDisposition, error) {
	return nil, nil
}

type stubTripStoreForDispute struct {
	trip *models.Trip
	err  error
}

func (s *stubTripStoreForDispute) GetByOrderID(_ context.Context, _ string) (*models.Trip, error) {
	return s.trip, s.err
}

type stubDEStoreForDispute struct {
	de  *models.DeliveryExecutive
	err error
}

func (s *stubDEStoreForDispute) GetByPhone(_ context.Context, _ string) (*models.DeliveryExecutive, error) {
	return s.de, s.err
}

func newAdminDisputeSvcWithEvidence(d *models.Dispute, trip *models.Trip, de *models.DeliveryExecutive) *AdminDisputeService {
	ds := &adminDetailDisputeStore{}
	if d != nil {
		ds.byID = map[string]*models.Dispute{d.DisputeID: d}
	}
	return &AdminDisputeService{
		disputes:     ds,
		dispositions: &adminDetailDispositionStore{},
		trips:        &stubTripStoreForDispute{trip: trip},
		riders:       &stubDEStoreForDispute{de: de},
	}
}

func TestGetDetail_DisputeNotFound(t *testing.T) {
	svc := newAdminDisputeSvcWithEvidence(nil, nil, nil)
	_, err := svc.GetDetail(context.Background(), "missing")
	if !errors.Is(err, ErrDisputeNotFound) {
		t.Fatalf("want ErrDisputeNotFound, got %v", err)
	}
}

func TestGetDetail_WithTripAndDE(t *testing.T) {
	d := &models.Dispute{
		DisputeID:       "disp-1",
		OrderNumber:     "ORD-001",
		CustomerID:      "cust-1",
		DispositionCode: "WRONG_ITEM",
		Status:          models.DisputeStatusOpen,
		CreatedAt:       "2026-06-24T10:00:00Z",
		UpdatedAt:       "2026-06-24T10:00:00Z",
	}
	trip := &models.Trip{
		TripID:  "trip-1",
		OrderID: "ORD-001",
		DEID:    "de-1",
		DEPhone: "+260971000001",
		Items:   []models.TripItem{{Name: "Widget", ImageURL: "items/w.jpg", Quantity: 2}},
		Tasks: []models.Task{
			{TaskID: "tp", Type: models.TaskTypePickup, Status: models.TaskStatusCompleted, CompletedAt: "2026-06-24T10:30:00Z"},
			{TaskID: "td", Type: models.TaskTypeDrop, Status: models.TaskStatusCompleted, CompletedAt: "2026-06-24T11:00:00Z", PhotoS3Key: "orders/ORD-001/drop/de-1/abc.jpg"},
		},
	}
	de := &models.DeliveryExecutive{
		DEID:                "de-1",
		Name:                "Chansa M.",
		PhoneNumber:         "+260971000001",
		TotalTripsCompleted: 42,
		CreatedAt:           "2026-01-01T00:00:00Z",
	}
	svc := newAdminDisputeSvcWithEvidence(d, trip, de)
	detail, err := svc.GetDetail(context.Background(), "disp-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.Dispute.DisputeID != "disp-1" {
		t.Errorf("wrong dispute_id: %q", detail.Dispute.DisputeID)
	}
	if detail.Trip == nil || detail.Trip.OrderID != "ORD-001" {
		t.Errorf("expected trip with ORD-001, got %v", detail.Trip)
	}
	if detail.DE == nil || detail.DE.DEID != "de-1" {
		t.Errorf("expected DE de-1, got %v", detail.DE)
	}
}

func TestGetDetail_NoTrip_FailOpen(t *testing.T) {
	d := &models.Dispute{
		DisputeID: "disp-2", OrderNumber: "ORD-999",
		CustomerID: "cust-1", DispositionCode: "WRONG_ITEM",
		Status:    models.DisputeStatusOpen,
		CreatedAt: "2026-06-24T10:00:00Z", UpdatedAt: "2026-06-24T10:00:00Z",
	}
	svc := newAdminDisputeSvcWithEvidence(d, nil, nil)
	detail, err := svc.GetDetail(context.Background(), "disp-2")
	if err != nil {
		t.Fatalf("unexpected error (should fail-open): %v", err)
	}
	if detail.Trip != nil {
		t.Errorf("expected nil trip, got %v", detail.Trip)
	}
}
