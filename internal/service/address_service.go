package service

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

var (
	ErrAddressNotFound = errors.New("address not found")
	ErrForbidden       = errors.New("you do not own this address")
)

const suggestRadiusMeters = 100.0

type AddressService struct {
	repo   *repository.AddressRepository
	logger *logrus.Logger
}

func NewAddressService(repo *repository.AddressRepository, logger *logrus.Logger) *AddressService {
	return &AddressService{
		repo:   repo,
		logger: logger,
	}
}

func (s *AddressService) CreateAddress(ctx context.Context, userID string, addr *models.Address) (*models.Address, error) {
	op := logging.Start(ctx, s.logger, "CreateAddress", logrus.Fields{"user_id": userID})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)
	addr.AddressID = uuid.New().String()
	addr.UserID = userID
	addr.IsActive = true
	addr.CreatedAt = now
	addr.UpdatedAt = now

	if addr.Tag == "" {
		addr.Tag = "other"
	}

	if err := s.repo.Create(ctx, addr); err != nil {
		return nil, op.Fail(err)
	}
	return addr, nil
}

func (s *AddressService) GetAddressByID(ctx context.Context, addressID, userID string) (*models.Address, error) {
	op := logging.Start(ctx, s.logger, "GetAddressByID", logrus.Fields{
		"address_id": addressID,
		"user_id":    userID,
	})
	defer op.End()

	addr, err := s.repo.GetByID(ctx, addressID)
	if err != nil {
		return nil, op.Fail(err)
	}

	if addr == nil || !addr.IsActive {
		return nil, op.Outcome("not_found", ErrAddressNotFound)
	}
	if addr.UserID != userID {
		return nil, op.Outcome("forbidden", ErrForbidden)
	}
	return addr, nil
}

func (s *AddressService) GetMyAddresses(ctx context.Context, userID string) ([]models.Address, error) {
	op := logging.Start(ctx, s.logger, "GetMyAddresses", logrus.Fields{"user_id": userID})
	defer op.End()

	addresses, err := s.repo.QueryByUserID(ctx, userID)
	if err != nil {
		return nil, op.Fail(err)
	}

	if addresses == nil {
		addresses = []models.Address{}
	}
	op.With("count", len(addresses))
	return addresses, nil
}

func (s *AddressService) UpdateReceiverDetails(ctx context.Context, addressID, userID string, updates map[string]string) (*models.Address, error) {
	op := logging.Start(ctx, s.logger, "UpdateReceiverDetails", logrus.Fields{
		"address_id": addressID,
		"user_id":    userID,
	})
	defer op.End()

	addr, err := s.repo.GetByID(ctx, addressID)
	if err != nil {
		return nil, op.Fail(err)
	}

	if addr == nil || !addr.IsActive {
		return nil, op.Outcome("not_found", ErrAddressNotFound)
	}
	if addr.UserID != userID {
		return nil, op.Outcome("forbidden", ErrForbidden)
	}

	updates["updated_at"] = time.Now().UTC().Format(time.RFC3339)

	updated, err := s.repo.UpdateReceiverDetails(ctx, addressID, updates)
	if err != nil {
		return nil, op.Fail(err)
	}
	return updated, nil
}

func (s *AddressService) RemoveAddress(ctx context.Context, addressID, userID string) error {
	op := logging.Start(ctx, s.logger, "RemoveAddress", logrus.Fields{
		"address_id": addressID,
		"user_id":    userID,
	})
	defer op.End()

	addr, err := s.repo.GetByID(ctx, addressID)
	if err != nil {
		return op.Fail(err)
	}

	if addr == nil || !addr.IsActive {
		return op.Outcome("not_found", ErrAddressNotFound)
	}
	if addr.UserID != userID {
		return op.Outcome("forbidden", ErrForbidden)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if err := s.repo.SoftDelete(ctx, addressID, now); err != nil {
		return op.Fail(err)
	}
	return nil
}

func (s *AddressService) GetSuggestedAddresses(ctx context.Context, userID string, lat, lng float64) ([]models.SuggestedAddress, error) {
	op := logging.Start(ctx, s.logger, "GetSuggestedAddresses", logrus.Fields{
		"user_id": userID,
		"lat":     lat,
		"lng":     lng,
	})
	defer op.End()

	addresses, err := s.repo.QueryByUserID(ctx, userID)
	if err != nil {
		return nil, op.Fail(err)
	}

	var suggested []models.SuggestedAddress
	for _, addr := range addresses {
		dist := models.HaversineDistance(lat, lng, addr.Latitude, addr.Longitude)
		if dist <= suggestRadiusMeters {
			suggested = append(suggested, models.SuggestedAddress{
				Address:        addr,
				DistanceMeters: math.Round(dist*10) / 10,
			})
		}
	}

	sort.Slice(suggested, func(i, j int) bool {
		return suggested[i].DistanceMeters < suggested[j].DistanceMeters
	})

	op.With("count", len(suggested))
	return suggested, nil
}
