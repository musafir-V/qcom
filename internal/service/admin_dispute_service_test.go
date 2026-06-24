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
	svc := NewAdminDisputeService(store, &fakeDispositionLookup{})
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
	svc := NewAdminDisputeService(store, &fakeDispositionLookup{})
	if _, err := svc.UpdateStatus(context.Background(), "d1", models.DisputeStatusUnderReview, "", "alice"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestAdminUpdateStatus_NoteRequiredForResolve(t *testing.T) {
	store := &fakeAdminDisputeStore{getByID: &models.Dispute{Status: models.DisputeStatusOpen}}
	svc := NewAdminDisputeService(store, &fakeDispositionLookup{})
	if _, err := svc.UpdateStatus(context.Background(), "d1", models.DisputeStatusRejected, "  ", "alice"); !errors.Is(err, ErrResolutionNoteRequired) {
		t.Fatalf("want ErrResolutionNoteRequired, got %v", err)
	}
}

func TestAdminUpdateStatus_InvalidTransition(t *testing.T) {
	store := &fakeAdminDisputeStore{getByID: &models.Dispute{Status: models.DisputeStatusResolved}}
	svc := NewAdminDisputeService(store, &fakeDispositionLookup{})
	if _, err := svc.UpdateStatus(context.Background(), "d1", models.DisputeStatusOpen, "x", "alice"); !errors.Is(err, ErrInvalidDisputeTransition) {
		t.Fatalf("want ErrInvalidDisputeTransition, got %v", err)
	}
}

func TestAdminUpdateStatus_NotFound(t *testing.T) {
	store := &fakeAdminDisputeStore{getByID: nil}
	svc := NewAdminDisputeService(store, &fakeDispositionLookup{})
	if _, err := svc.UpdateStatus(context.Background(), "d1", models.DisputeStatusResolved, "x", "alice"); !errors.Is(err, ErrDisputeNotFound) {
		t.Fatalf("want ErrDisputeNotFound, got %v", err)
	}
}

func TestAdminUpdateStatus_InvalidStatus(t *testing.T) {
	svc := NewAdminDisputeService(&fakeAdminDisputeStore{}, &fakeDispositionLookup{})
	if _, err := svc.UpdateStatus(context.Background(), "d1", models.DisputeStatus("BOGUS"), "x", "alice"); !errors.Is(err, ErrInvalidDisputeStatus) {
		t.Fatalf("want ErrInvalidDisputeStatus, got %v", err)
	}
}

func TestAdminListByStatus_InvalidStatus(t *testing.T) {
	svc := NewAdminDisputeService(&fakeAdminDisputeStore{}, &fakeDispositionLookup{})
	if _, _, err := svc.ListByStatus(context.Background(), models.DisputeStatus("NOPE"), "", 50); !errors.Is(err, ErrInvalidDisputeStatus) {
		t.Fatalf("want ErrInvalidDisputeStatus, got %v", err)
	}
}

func TestAdminSummary(t *testing.T) {
	store := &fakeAdminDisputeStore{counts: map[models.DisputeStatus]int{
		models.DisputeStatusOpen: 3, models.DisputeStatusUnderReview: 1,
		models.DisputeStatusResolved: 7, models.DisputeStatusRejected: 2,
	}}
	svc := NewAdminDisputeService(store, &fakeDispositionLookup{})
	got, err := svc.Summary(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Open != 3 || got.UnderReview != 1 || got.Resolved != 7 || got.Rejected != 2 {
		t.Errorf("summary = %+v", got)
	}
}
