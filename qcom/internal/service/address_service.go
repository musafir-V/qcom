package service

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
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
	now := time.Now().UTC().Format(time.RFC3339)
	addr.AddressID = uuid.New().String()
	addr.UserID = userID
	addr.IsActive = true
	addr.CreatedAt = now
	addr.UpdatedAt = now

	if addr.Label == "" {
		addr.Label = "other"
	}

	if err := s.repo.Create(ctx, addr); err != nil {
		return nil, err
	}

	return addr, nil
}

func (s *AddressService) GetAddressByID(ctx context.Context, addressID, userID string) (*models.Address, error) {
	addr, err := s.repo.GetByID(ctx, addressID)
	if err != nil {
		return nil, err
	}

	if addr == nil || !addr.IsActive {
		return nil, ErrAddressNotFound
	}

	if addr.UserID != userID {
		return nil, ErrForbidden
	}

	return addr, nil
}

func (s *AddressService) GetMyAddresses(ctx context.Context, userID string) ([]models.Address, error) {
	addresses, err := s.repo.QueryByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}

	if addresses == nil {
		addresses = []models.Address{}
	}

	return addresses, nil
}

func (s *AddressService) UpdateReceiverDetails(ctx context.Context, addressID, userID string, updates map[string]string) (*models.Address, error) {
	addr, err := s.repo.GetByID(ctx, addressID)
	if err != nil {
		return nil, err
	}

	if addr == nil || !addr.IsActive {
		return nil, ErrAddressNotFound
	}

	if addr.UserID != userID {
		return nil, ErrForbidden
	}

	updates["updated_at"] = time.Now().UTC().Format(time.RFC3339)

	updated, err := s.repo.UpdateReceiverDetails(ctx, addressID, updates)
	if err != nil {
		return nil, err
	}

	return updated, nil
}

func (s *AddressService) RemoveAddress(ctx context.Context, addressID, userID string) error {
	addr, err := s.repo.GetByID(ctx, addressID)
	if err != nil {
		return err
	}

	if addr == nil || !addr.IsActive {
		return ErrAddressNotFound
	}

	if addr.UserID != userID {
		return ErrForbidden
	}

	now := time.Now().UTC().Format(time.RFC3339)
	return s.repo.SoftDelete(ctx, addressID, now)
}

func (s *AddressService) GetSuggestedAddresses(ctx context.Context, userID string, lat, lng float64) ([]models.SuggestedAddress, error) {
	addresses, err := s.repo.QueryByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}

	var suggested []models.SuggestedAddress
	for _, addr := range addresses {
		dist := models.HaversineDistance(lat, lng, addr.Latitude, addr.Longitude)
		if dist <= suggestRadiusMeters {
			suggested = append(suggested, models.SuggestedAddress{
				Address:        addr,
				DistanceMeters: math.Round(dist*10) / 10, // round to 1 decimal
			})
		}
	}

	sort.Slice(suggested, func(i, j int) bool {
		return suggested[i].DistanceMeters < suggested[j].DistanceMeters
	})

	return suggested, nil
}
