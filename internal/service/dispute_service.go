// internal/service/dispute_service.go
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

// orderStatusNotFound is the sentinel JavaOrderClient.GetOrderStatus returns on a 404.
const orderStatusNotFound = "NOT_FOUND"

var (
	ErrOrderNotFound       = errors.New("order not found")
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
	GetLatestByOrderNumber(ctx context.Context, orderNumber string) (*models.Dispute, error)
}

type dispositionStore interface {
	ListActive(ctx context.Context) ([]models.DisputeDisposition, error)
	GetByCode(ctx context.Context, code string) (*models.DisputeDisposition, error)
}

// orderValidator is satisfied by *JavaOrderClient.
type orderValidator interface {
	GetOrderStatus(ctx context.Context, orderID string) (string, error)
}

type DisputeService struct {
	disputes         disputeStore
	dispositions     dispositionStore
	orders           orderValidator
	trips            tripStoreForDispute
	notifier         DisputeNotifier
	eligibleStatuses map[string]bool
	logger           *logrus.Logger
}

func NewDisputeService(disputes disputeStore, dispositions dispositionStore, orders orderValidator, trips tripStoreForDispute, notifier DisputeNotifier, eligibleStatuses []string, logger *logrus.Logger) *DisputeService {
	set := make(map[string]bool, len(eligibleStatuses))
	for _, s := range eligibleStatuses {
		set[strings.ToUpper(strings.TrimSpace(s))] = true
	}
	return &DisputeService{
		disputes:         disputes,
		dispositions:     dispositions,
		orders:           orders,
		trips:            trips,
		notifier:         notifier,
		eligibleStatuses: set,
		logger:           logger,
	}
}

type CreateDisputeInput struct {
	CustomerID      string
	OrderNumber     string
	DispositionCode string
	Description     string
	PhotoKeys       []string
}

func (s *DisputeService) ListDispositions(ctx context.Context) ([]models.DisputeDisposition, error) {
	return s.dispositions.ListActive(ctx)
}

func (s *DisputeService) CreateDispute(ctx context.Context, in CreateDisputeInput) (*models.Dispute, error) {
	// 1. Validate order exists and status eligibility via order-service public API.
	status, err := s.orders.GetOrderStatus(ctx, in.OrderNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch order status: %w", err)
	}
	if status == orderStatusNotFound {
		return nil, ErrOrderNotFound
	}
	if !s.eligibleStatuses[strings.ToUpper(status)] {
		return nil, ErrOrderNotDisputable
	}

	// 2. Validate disposition.
	disp, err := s.dispositions.GetByCode(ctx, in.DispositionCode)
	if err != nil {
		return nil, fmt.Errorf("failed to load disposition: %w", err)
	}
	if disp == nil || !disp.Active {
		return nil, ErrDispositionNotFound
	}

	// 3. Validate description against disposition rules.
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

	// 4. Validate photos: count, min, and per-key ownership prefix.
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

	// 5. Persist (transactional one-open guard inside the repo).
	now := time.Now().UTC().Format(time.RFC3339)
	d := &models.Dispute{
		OrderNumber:     in.OrderNumber,
		CustomerID:      in.CustomerID,
		DispositionCode: in.DispositionCode,
		Description:     desc,
		PhotoKeys:       in.PhotoKeys,
		Status:          models.DisputeStatusOpen,
		StoreID:         s.resolveStoreID(ctx, in.OrderNumber),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.disputes.Create(ctx, d); err != nil {
		if errors.Is(err, repository.ErrDisputeAlreadyOpen) {
			return nil, ErrDisputeAlreadyOpen
		}
		return nil, fmt.Errorf("failed to create dispute: %w", err)
	}

	// 6. Emit creation seam (best-effort).
	s.notifier.DisputeCreated(ctx, d)
	return d, nil
}

// resolveStoreID finds the darkstore for a disputed order via its trip.
//
// This is safe because disputes are only allowed on orders in
// DISPUTE_ELIGIBLE_ORDER_STATUSES, which defaults to DELIVERED (see
// config.go), and every route to DELIVERED goes through a trip — the
// driver-app drop flow and the admin drop/pickup overrides alike. If that env
// var is ever widened to a pre-dispatch status, disputes on those orders will
// silently start resolving to UNKNOWN.
//
// Fails open: a missing trip, a lookup error, or an empty Trip.StoreID all
// yield UnknownStoreID. A customer's complaint is never rejected because we
// could not attribute it to a store.
func (s *DisputeService) resolveStoreID(ctx context.Context, orderNumber string) string {
	if s.trips == nil {
		return models.UnknownStoreID
	}
	trip, err := s.trips.GetByOrderID(ctx, orderNumber)
	if err != nil {
		s.logger.WithError(err).WithField("order_number", orderNumber).
			Warn("failed to resolve store for dispute; falling back to UNKNOWN")
		return models.UnknownStoreID
	}
	if trip == nil || trip.StoreID == "" {
		return models.UnknownStoreID
	}
	return trip.StoreID
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

func (s *DisputeService) GetDisputeByOrder(ctx context.Context, customerID, orderNumber string) (*models.Dispute, error) {
	d, err := s.disputes.GetLatestByOrderNumber(ctx, orderNumber)
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
