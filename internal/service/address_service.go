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
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":      "CreateAddress",
		"user_id": userID,
	}).Info("service call start")

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
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "CreateAddress",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return nil, err
	}

	log.WithFields(logrus.Fields{
		"op":          "CreateAddress",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("service call done")
	return addr, nil
}

func (s *AddressService) GetAddressByID(ctx context.Context, addressID, userID string) (*models.Address, error) {
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":         "GetAddressByID",
		"address_id": addressID,
		"user_id":    userID,
	}).Info("service call start")

	addr, err := s.repo.GetByID(ctx, addressID)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GetAddressByID",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return nil, err
	}

	if addr == nil || !addr.IsActive {
		log.WithFields(logrus.Fields{
			"op":          "GetAddressByID",
			"duration_ms": time.Since(start).Milliseconds(),
			"outcome":     "not_found",
		}).Info("service call done")
		return nil, ErrAddressNotFound
	}

	if addr.UserID != userID {
		log.WithFields(logrus.Fields{
			"op":          "GetAddressByID",
			"duration_ms": time.Since(start).Milliseconds(),
			"outcome":     "forbidden",
		}).Info("service call done")
		return nil, ErrForbidden
	}

	log.WithFields(logrus.Fields{
		"op":          "GetAddressByID",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("service call done")
	return addr, nil
}

func (s *AddressService) GetMyAddresses(ctx context.Context, userID string) ([]models.Address, error) {
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":      "GetMyAddresses",
		"user_id": userID,
	}).Info("service call start")

	addresses, err := s.repo.QueryByUserID(ctx, userID)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GetMyAddresses",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return nil, err
	}

	if addresses == nil {
		addresses = []models.Address{}
	}

	log.WithFields(logrus.Fields{
		"op":          "GetMyAddresses",
		"duration_ms": time.Since(start).Milliseconds(),
		"count":       len(addresses),
	}).Info("service call done")
	return addresses, nil
}

func (s *AddressService) UpdateReceiverDetails(ctx context.Context, addressID, userID string, updates map[string]string) (*models.Address, error) {
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":         "UpdateReceiverDetails",
		"address_id": addressID,
		"user_id":    userID,
	}).Info("service call start")

	addr, err := s.repo.GetByID(ctx, addressID)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "UpdateReceiverDetails",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return nil, err
	}

	if addr == nil || !addr.IsActive {
		log.WithFields(logrus.Fields{
			"op":          "UpdateReceiverDetails",
			"duration_ms": time.Since(start).Milliseconds(),
			"outcome":     "not_found",
		}).Info("service call done")
		return nil, ErrAddressNotFound
	}

	if addr.UserID != userID {
		log.WithFields(logrus.Fields{
			"op":          "UpdateReceiverDetails",
			"duration_ms": time.Since(start).Milliseconds(),
			"outcome":     "forbidden",
		}).Info("service call done")
		return nil, ErrForbidden
	}

	updates["updated_at"] = time.Now().UTC().Format(time.RFC3339)

	updated, err := s.repo.UpdateReceiverDetails(ctx, addressID, updates)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "UpdateReceiverDetails",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return nil, err
	}

	log.WithFields(logrus.Fields{
		"op":          "UpdateReceiverDetails",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("service call done")
	return updated, nil
}

func (s *AddressService) RemoveAddress(ctx context.Context, addressID, userID string) error {
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":         "RemoveAddress",
		"address_id": addressID,
		"user_id":    userID,
	}).Info("service call start")

	addr, err := s.repo.GetByID(ctx, addressID)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "RemoveAddress",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return err
	}

	if addr == nil || !addr.IsActive {
		log.WithFields(logrus.Fields{
			"op":          "RemoveAddress",
			"duration_ms": time.Since(start).Milliseconds(),
			"outcome":     "not_found",
		}).Info("service call done")
		return ErrAddressNotFound
	}

	if addr.UserID != userID {
		log.WithFields(logrus.Fields{
			"op":          "RemoveAddress",
			"duration_ms": time.Since(start).Milliseconds(),
			"outcome":     "forbidden",
		}).Info("service call done")
		return ErrForbidden
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if err := s.repo.SoftDelete(ctx, addressID, now); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "RemoveAddress",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return err
	}

	log.WithFields(logrus.Fields{
		"op":          "RemoveAddress",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("service call done")
	return nil
}

func (s *AddressService) GetSuggestedAddresses(ctx context.Context, userID string, lat, lng float64) ([]models.SuggestedAddress, error) {
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":      "GetSuggestedAddresses",
		"user_id": userID,
		"lat":     lat,
		"lng":     lng,
	}).Info("service call start")

	addresses, err := s.repo.QueryByUserID(ctx, userID)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GetSuggestedAddresses",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return nil, err
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

	log.WithFields(logrus.Fields{
		"op":          "GetSuggestedAddresses",
		"duration_ms": time.Since(start).Milliseconds(),
		"count":       len(suggested),
	}).Info("service call done")
	return suggested, nil
}
