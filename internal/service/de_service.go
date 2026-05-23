package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

type DEService struct {
	deRepo    *repository.DERepository
	qrService *QRService
	logger    *logrus.Logger
}

func NewDEService(deRepo *repository.DERepository, qrService *QRService, logger *logrus.Logger) *DEService {
	return &DEService{deRepo: deRepo, qrService: qrService, logger: logger}
}

type RegisterDERequest struct {
	PhoneNumber string
	Name        string
	ProfileURL  string
	NRCURL      string
}

func (s *DEService) Register(ctx context.Context, req RegisterDERequest) (*models.DeliveryExecutive, error) {
	de := &models.DeliveryExecutive{
		DEID:        uuid.New().String(),
		PhoneNumber: req.PhoneNumber,
		Name:        req.Name,
		ProfileURL:  req.ProfileURL,
		NRCURL:      req.NRCURL,
		Status:      models.DEStatusOffline,
	}

	if err := s.deRepo.Create(ctx, de); err != nil {
		return nil, err
	}
	return de, nil
}

func (s *DEService) GetDE(ctx context.Context, phone string) (*models.DeliveryExecutive, error) {
	de, err := s.deRepo.GetByPhone(ctx, phone)
	if err != nil {
		return nil, err
	}
	if de == nil {
		return nil, fmt.Errorf("delivery executive not found")
	}
	return de, nil
}

// StartDuty validates the QR code and transitions the DE to eligible status.
// Valid from: offline or free.
func (s *DEService) StartDuty(ctx context.Context, dePhone, qrCode string) (string, error) {
	de, err := s.deRepo.GetByPhone(ctx, dePhone)
	if err != nil {
		return "", fmt.Errorf("failed to fetch DE: %w", err)
	}
	if de == nil {
		return "", fmt.Errorf("delivery executive not found")
	}

	if de.Status == models.DEStatusBusy {
		return "", fmt.Errorf("cannot start duty while on an active delivery")
	}
	if de.Status == models.DEStatusEligible {
		return "", fmt.Errorf("already on duty at store %s", de.CurrentStoreID)
	}

	storeID, err := s.qrService.ParseStoreID(qrCode)
	if err != nil {
		return "", fmt.Errorf("invalid QR code: %w", err)
	}

	if err := s.qrService.ValidateQRCode(qrCode, storeID); err != nil {
		return "", err
	}

	if err := s.deRepo.UpdateStatus(ctx, dePhone, models.DEStatusEligible, storeID, ""); err != nil {
		return "", fmt.Errorf("failed to update DE status: %w", err)
	}

	s.logger.WithFields(logrus.Fields{
		"de_phone": dePhone,
		"store_id": storeID,
	}).Info("DE started duty")

	return storeID, nil
}
