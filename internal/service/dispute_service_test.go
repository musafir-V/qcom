// internal/service/dispute_service_test.go
package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

type stubDisputeStore struct {
	created   *models.Dispute
	createErr error
	byID      *models.Dispute
	byOrder   *models.Dispute
}

func (s *stubDisputeStore) Create(_ context.Context, d *models.Dispute) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.created = d
	return nil
}
func (s *stubDisputeStore) GetByID(_ context.Context, id string) (*models.Dispute, error) {
	return s.byID, nil
}
func (s *stubDisputeStore) GetLatestByOrderNumber(_ context.Context, orderNumber string) (*models.Dispute, error) {
	return s.byOrder, nil
}

type stubDispositionStore struct {
	disp *models.DisputeDisposition
}

func (s *stubDispositionStore) ListActive(_ context.Context) ([]models.DisputeDisposition, error) {
	if s.disp == nil {
		return nil, nil
	}
	return []models.DisputeDisposition{*s.disp}, nil
}
func (s *stubDispositionStore) GetByCode(_ context.Context, code string) (*models.DisputeDisposition, error) {
	if s.disp != nil && s.disp.Code == code {
		return s.disp, nil
	}
	return nil, nil
}

type stubOrderValidator struct {
	target *OrderNotificationTarget
	status string
}

func (s *stubOrderValidator) GetNotificationTarget(_ context.Context, _ string) (*OrderNotificationTarget, error) {
	return s.target, nil
}
func (s *stubOrderValidator) GetOrderStatus(_ context.Context, _ string) (string, error) {
	return s.status, nil
}

type noopNotifier struct{}

func (noopNotifier) DisputeCreated(_ context.Context, _ *models.Dispute) {}

func newTestDisputeService(ds *stubDisputeStore, dp *stubDispositionStore, ov *stubOrderValidator) *DisputeService {
	return NewDisputeService(ds, dp, ov, noopNotifier{}, []string{"DELIVERED"}, logrus.New())
}

func basicDisposition() *models.DisputeDisposition {
	return &models.DisputeDisposition{Code: "ITEM_MISSING", Title: "Item missing", Active: true}
}

func ownedDelivered(customerID string) *stubOrderValidator {
	return &stubOrderValidator{
		target: &OrderNotificationTarget{CustomerID: customerID},
		status: "DELIVERED",
	}
}

func validInput() CreateDisputeInput {
	return CreateDisputeInput{
		CustomerID:      "cust-1",
		OrderNumber:     "ORD123",
		DispositionCode: "ITEM_MISSING",
		Description:     "two items were not in the bag",
		PhotoKeys:       []string{"disputes/cust-1/abc.jpg"},
	}
}

func TestCreateDispute_Success(t *testing.T) {
	ds := &stubDisputeStore{}
	svc := newTestDisputeService(ds, &stubDispositionStore{disp: basicDisposition()}, ownedDelivered("cust-1"))
	got, err := svc.CreateDispute(context.Background(), validInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Status != models.DisputeStatusOpen {
		t.Errorf("status = %q, want OPEN", got.Status)
	}
	if ds.created == nil || ds.created.DisputeID == "" {
		t.Error("expected dispute persisted with an id")
	}
}

func TestCreateDispute_OrderNotFound(t *testing.T) {
	svc := newTestDisputeService(&stubDisputeStore{}, &stubDispositionStore{disp: basicDisposition()}, &stubOrderValidator{target: nil})
	_, err := svc.CreateDispute(context.Background(), validInput())
	if !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("want ErrOrderNotFound, got %v", err)
	}
}

func TestCreateDispute_NotOwner(t *testing.T) {
	ov := &stubOrderValidator{target: &OrderNotificationTarget{CustomerID: "someone-else"}, status: "DELIVERED"}
	svc := newTestDisputeService(&stubDisputeStore{}, &stubDispositionStore{disp: basicDisposition()}, ov)
	_, err := svc.CreateDispute(context.Background(), validInput())
	if !errors.Is(err, ErrNotOrderOwner) {
		t.Fatalf("want ErrNotOrderOwner, got %v", err)
	}
}

func TestCreateDispute_NotDisputableStatus(t *testing.T) {
	ov := &stubOrderValidator{target: &OrderNotificationTarget{CustomerID: "cust-1"}, status: "OUT_FOR_DELIVERY"}
	svc := newTestDisputeService(&stubDisputeStore{}, &stubDispositionStore{disp: basicDisposition()}, ov)
	_, err := svc.CreateDispute(context.Background(), validInput())
	if !errors.Is(err, ErrOrderNotDisputable) {
		t.Fatalf("want ErrOrderNotDisputable, got %v", err)
	}
}

func TestCreateDispute_UnknownDisposition(t *testing.T) {
	svc := newTestDisputeService(&stubDisputeStore{}, &stubDispositionStore{disp: nil}, ownedDelivered("cust-1"))
	_, err := svc.CreateDispute(context.Background(), validInput())
	if !errors.Is(err, ErrDispositionNotFound) {
		t.Fatalf("want ErrDispositionNotFound, got %v", err)
	}
}

func TestCreateDispute_DescriptionRequired(t *testing.T) {
	disp := basicDisposition()
	disp.DescriptionRequired = true
	in := validInput()
	in.Description = ""
	svc := newTestDisputeService(&stubDisputeStore{}, &stubDispositionStore{disp: disp}, ownedDelivered("cust-1"))
	_, err := svc.CreateDispute(context.Background(), in)
	if !errors.Is(err, ErrDescriptionRequired) {
		t.Fatalf("want ErrDescriptionRequired, got %v", err)
	}
}

func TestCreateDispute_TooManyPhotos(t *testing.T) {
	in := validInput()
	in.PhotoKeys = []string{"disputes/cust-1/a.jpg", "disputes/cust-1/b.jpg", "disputes/cust-1/c.jpg", "disputes/cust-1/d.jpg"}
	svc := newTestDisputeService(&stubDisputeStore{}, &stubDispositionStore{disp: basicDisposition()}, ownedDelivered("cust-1"))
	_, err := svc.CreateDispute(context.Background(), in)
	if !errors.Is(err, ErrTooManyPhotos) {
		t.Fatalf("want ErrTooManyPhotos, got %v", err)
	}
}

func TestCreateDispute_PhotoKeyWrongOwner(t *testing.T) {
	in := validInput()
	in.PhotoKeys = []string{"disputes/other-cust/a.jpg"}
	svc := newTestDisputeService(&stubDisputeStore{}, &stubDispositionStore{disp: basicDisposition()}, ownedDelivered("cust-1"))
	_, err := svc.CreateDispute(context.Background(), in)
	if !errors.Is(err, ErrInvalidPhotoKey) {
		t.Fatalf("want ErrInvalidPhotoKey, got %v", err)
	}
}

func TestCreateDispute_PhotoMinNotMet(t *testing.T) {
	disp := basicDisposition()
	disp.PhotosRequired = true
	disp.PhotoMin = 1
	in := validInput()
	in.PhotoKeys = nil
	svc := newTestDisputeService(&stubDisputeStore{}, &stubDispositionStore{disp: disp}, ownedDelivered("cust-1"))
	_, err := svc.CreateDispute(context.Background(), in)
	if !errors.Is(err, ErrNotEnoughPhotos) {
		t.Fatalf("want ErrNotEnoughPhotos, got %v", err)
	}
}

func TestCreateDispute_AlreadyOpenConflict(t *testing.T) {
	ds := &stubDisputeStore{createErr: repository.ErrDisputeAlreadyOpen}
	svc := newTestDisputeService(ds, &stubDispositionStore{disp: basicDisposition()}, ownedDelivered("cust-1"))
	_, err := svc.CreateDispute(context.Background(), validInput())
	if !errors.Is(err, ErrDisputeAlreadyOpen) {
		t.Fatalf("want ErrDisputeAlreadyOpen, got %v", err)
	}
}

func TestGetDispute_OwnershipEnforced(t *testing.T) {
	ds := &stubDisputeStore{byID: &models.Dispute{DisputeID: "d1", CustomerID: "owner"}}
	svc := newTestDisputeService(ds, &stubDispositionStore{}, &stubOrderValidator{})
	_, err := svc.GetDispute(context.Background(), "intruder", "d1")
	if !errors.Is(err, ErrDisputeForbidden) {
		t.Fatalf("want ErrDisputeForbidden, got %v", err)
	}
}

func TestGetDisputeByOrder_NotFound(t *testing.T) {
	svc := newTestDisputeService(&stubDisputeStore{byOrder: nil}, &stubDispositionStore{}, &stubOrderValidator{})
	_, err := svc.GetDisputeByOrder(context.Background(), "cust-1", "order-x")
	if !errors.Is(err, ErrDisputeNotFound) {
		t.Fatalf("want ErrDisputeNotFound, got %v", err)
	}
}

func TestCreateDispute_OrderStatusNotFound(t *testing.T) {
	ov := &stubOrderValidator{
		target: &OrderNotificationTarget{CustomerID: "cust-1"},
		status: "NOT_FOUND",
	}
	svc := newTestDisputeService(&stubDisputeStore{}, &stubDispositionStore{disp: basicDisposition()}, ov)
	_, err := svc.CreateDispute(context.Background(), validInput())
	if !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("want ErrOrderNotFound, got %v", err)
	}
}

func TestCreateDispute_DescriptionTooShort(t *testing.T) {
	disp := basicDisposition()
	disp.DescriptionRequired = true
	in := validInput()
	in.Description = "short"
	svc := newTestDisputeService(&stubDisputeStore{}, &stubDispositionStore{disp: disp}, ownedDelivered("cust-1"))
	_, err := svc.CreateDispute(context.Background(), in)
	if !errors.Is(err, ErrDescriptionTooShort) {
		t.Fatalf("want ErrDescriptionTooShort, got %v", err)
	}
}

func TestCreateDispute_DescriptionTooLong(t *testing.T) {
	in := validInput()
	in.Description = strings.Repeat("a", 501)
	svc := newTestDisputeService(&stubDisputeStore{}, &stubDispositionStore{disp: basicDisposition()}, ownedDelivered("cust-1"))
	_, err := svc.CreateDispute(context.Background(), in)
	if !errors.Is(err, ErrDescriptionTooLong) {
		t.Fatalf("want ErrDescriptionTooLong, got %v", err)
	}
}

func TestGetDisputeByOrder_Forbidden(t *testing.T) {
	ds := &stubDisputeStore{byOrder: &models.Dispute{DisputeID: "d2", CustomerID: "owner"}}
	svc := newTestDisputeService(ds, &stubDispositionStore{}, &stubOrderValidator{})
	_, err := svc.GetDisputeByOrder(context.Background(), "intruder", "order-1")
	if !errors.Is(err, ErrDisputeForbidden) {
		t.Fatalf("want ErrDisputeForbidden, got %v", err)
	}
}
