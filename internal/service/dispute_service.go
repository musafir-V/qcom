// internal/service/dispute_service.go
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

// orderStatusNotFound is the sentinel JavaOrderClient.GetOrderStatus returns on a 404.
const orderStatusNotFound = "NOT_FOUND"

var (
	ErrOrderNotFound       = errors.New("order not found")
	ErrNotOrderOwner       = errors.New("order does not belong to customer")
	ErrOrderNotDisputable  = errors.New("order is not in a disputable state")
	ErrDispositionNotFound = errors.New("disposition not found")
	ErrDescriptionRequired = errors.New("description is required for this disposition")
	ErrDescriptionTooShort = errors.New("description is too short")
	ErrDescriptionTooLong  = errors.New("description is too long")
	ErrTooManyPhotos       = errors.New("too many photos")
	ErrNotEnoughPhotos     = errors.New("not enough photos for this disposition")
	ErrInvalidPhotoKey     = errors.New("photo key is not owned by this customer")
	ErrDisputeAlreadyOpen  = repository.ErrDisputeAlreadyOpen
	ErrDisputeNotFound     = errors.New("dispute not found")
	ErrDisputeForbidden    = errors.New("dispute does not belong to customer")
)

type disputeStore interface {
	Create(ctx context.Context, d *models.Dispute) error
	GetByID(ctx context.Context, id string) (*models.Dispute, error)
	GetLatestByOrderID(ctx context.Context, orderID string) (*models.Dispute, error)
}

type dispositionStore interface {
	ListActive(ctx context.Context) ([]models.DisputeDisposition, error)
	GetByCode(ctx context.Context, code string) (*models.DisputeDisposition, error)
}

// orderValidator is satisfied by *JavaOrderClient.
type orderValidator interface {
	GetNotificationTarget(ctx context.Context, orderRef string) (*OrderNotificationTarget, error)
	GetOrderStatus(ctx context.Context, orderID string) (string, error)
}

type DisputeService struct {
	disputes         disputeStore
	dispositions     dispositionStore
	orders           orderValidator
	notifier         DisputeNotifier
	eligibleStatuses map[string]bool
	logger           *logrus.Logger
}

func NewDisputeService(disputes disputeStore, dispositions dispositionStore, orders orderValidator, notifier DisputeNotifier, eligibleStatuses []string, logger *logrus.Logger) *DisputeService {
	set := make(map[string]bool, len(eligibleStatuses))
	for _, s := range eligibleStatuses {
		set[strings.ToUpper(strings.TrimSpace(s))] = true
	}
	return &DisputeService{
		disputes:         disputes,
		dispositions:     dispositions,
		orders:           orders,
		notifier:         notifier,
		eligibleStatuses: set,
		logger:           logger,
	}
}

type CreateDisputeInput struct {
	CustomerID      string
	OrderID         string
	DispositionCode string
	Description     string
	PhotoKeys       []string
}

func (s *DisputeService) ListDispositions(ctx context.Context) ([]models.DisputeDisposition, error) {
	return s.dispositions.ListActive(ctx)
}

func (s *DisputeService) CreateDispute(ctx context.Context, in CreateDisputeInput) (*models.Dispute, error) {
	// 1. Validate order ownership.
	target, err := s.orders.GetNotificationTarget(ctx, in.OrderID)
	if err != nil {
		return nil, fmt.Errorf("failed to validate order: %w", err)
	}
	if target == nil {
		return nil, ErrOrderNotFound
	}
	if target.CustomerID != in.CustomerID {
		return nil, ErrNotOrderOwner
	}

	// 2. Validate order status eligibility.
	status, err := s.orders.GetOrderStatus(ctx, in.OrderID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch order status: %w", err)
	}
	if status == orderStatusNotFound {
		return nil, ErrOrderNotFound
	}
	if !s.eligibleStatuses[strings.ToUpper(status)] {
		return nil, ErrOrderNotDisputable
	}

	// 3. Validate disposition.
	disp, err := s.dispositions.GetByCode(ctx, in.DispositionCode)
	if err != nil {
		return nil, fmt.Errorf("failed to load disposition: %w", err)
	}
	if disp == nil || !disp.Active {
		return nil, ErrDispositionNotFound
	}

	// 4. Validate description against disposition rules.
	desc := strings.TrimSpace(in.Description)
	if disp.DescriptionRequired && desc == "" {
		return nil, ErrDescriptionRequired
	}
	if desc != "" {
		if len(desc) < models.DisputeDescriptionMinLen {
			return nil, ErrDescriptionTooShort
		}
		if len(desc) > models.DisputeDescriptionMaxLen {
			return nil, ErrDescriptionTooLong
		}
	}

	// 5. Validate photos: count, min, and per-key ownership prefix.
	if len(in.PhotoKeys) > models.MaxDisputePhotos {
		return nil, ErrTooManyPhotos
	}
	wantPrefix := models.DisputePhotoKeyPrefix + "/" + in.CustomerID + "/"
	for _, k := range in.PhotoKeys {
		if !strings.HasPrefix(k, wantPrefix) {
			return nil, ErrInvalidPhotoKey
		}
	}
	minPhotos := disp.PhotoMin
	if disp.PhotosRequired && minPhotos < 1 {
		minPhotos = 1
	}
	if len(in.PhotoKeys) < minPhotos {
		return nil, ErrNotEnoughPhotos
	}

	// 6. Persist (transactional one-open guard inside the repo).
	now := time.Now().UTC().Format(time.RFC3339)
	d := &models.Dispute{
		DisputeID:       uuid.New().String(),
		OrderID:         in.OrderID,
		CustomerID:      in.CustomerID,
		DispositionCode: in.DispositionCode,
		Description:     desc,
		PhotoKeys:       in.PhotoKeys,
		Status:          models.DisputeStatusOpen,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.disputes.Create(ctx, d); err != nil {
		if errors.Is(err, repository.ErrDisputeAlreadyOpen) {
			return nil, ErrDisputeAlreadyOpen
		}
		return nil, fmt.Errorf("failed to create dispute: %w", err)
	}

	// 7. Emit creation seam (best-effort).
	s.notifier.DisputeCreated(ctx, d)
	return d, nil
}

func (s *DisputeService) GetDispute(ctx context.Context, customerID, disputeID string) (*models.Dispute, error) {
	d, err := s.disputes.GetByID(ctx, disputeID)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, ErrDisputeNotFound
	}
	if d.CustomerID != customerID {
		return nil, ErrDisputeForbidden
	}
	return d, nil
}

func (s *DisputeService) GetDisputeByOrder(ctx context.Context, customerID, orderID string) (*models.Dispute, error) {
	d, err := s.disputes.GetLatestByOrderID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, ErrDisputeNotFound
	}
	if d.CustomerID != customerID {
		return nil, ErrDisputeForbidden
	}
	return d, nil
}
