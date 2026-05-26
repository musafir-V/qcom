package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/logging"
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
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":    "Register",
		"phone": req.PhoneNumber,
	}).Info("service call start")

	de := &models.DeliveryExecutive{
		DEID:        uuid.New().String(),
		PhoneNumber: req.PhoneNumber,
		Name:        req.Name,
		ProfileURL:  req.ProfileURL,
		NRCURL:      req.NRCURL,
		Status:      models.DEStatusOffline,
	}

	if err := s.deRepo.Create(ctx, de); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "Register",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return nil, err
	}

	log.WithFields(logrus.Fields{
		"op":          "Register",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("service call done")
	return de, nil
}

func (s *DEService) GetDE(ctx context.Context, phone string) (*models.DeliveryExecutive, error) {
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":    "GetDE",
		"phone": phone,
	}).Info("service call start")

	de, err := s.deRepo.GetByPhone(ctx, phone)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "GetDE",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return nil, err
	}
	if de == nil {
		log.WithFields(logrus.Fields{
			"op":          "GetDE",
			"duration_ms": time.Since(start).Milliseconds(),
			"outcome":     "not_found",
		}).Info("service call done")
		return nil, fmt.Errorf("delivery executive not found")
	}

	log.WithFields(logrus.Fields{
		"op":          "GetDE",
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("service call done")
	return de, nil
}

// StartDuty validates the QR code and transitions the DE to eligible status.
// Valid from: offline or free.
func (s *DEService) StartDuty(ctx context.Context, dePhone, qrCode string) (string, error) {
	log := logging.FromContext(ctx, s.logger)
	start := time.Now()
	log.WithFields(logrus.Fields{
		"op":    "StartDuty",
		"phone": dePhone,
	}).Info("service call start")

	de, err := s.deRepo.GetByPhone(ctx, dePhone)
	if err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "StartDuty",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return "", fmt.Errorf("failed to fetch DE: %w", err)
	}
	if de == nil {
		log.WithFields(logrus.Fields{
			"op":          "StartDuty",
			"duration_ms": time.Since(start).Milliseconds(),
			"outcome":     "not_found",
		}).Info("service call done")
		return "", fmt.Errorf("delivery executive not found")
	}

	if de.Status == models.DEStatusBusy {
		log.WithFields(logrus.Fields{
			"op":          "StartDuty",
			"duration_ms": time.Since(start).Milliseconds(),
			"outcome":     "busy",
		}).Info("service call done")
		return "", fmt.Errorf("cannot start duty while on an active delivery")
	}
	if de.Status == models.DEStatusEligible {
		log.WithFields(logrus.Fields{
			"op":          "StartDuty",
			"duration_ms": time.Since(start).Milliseconds(),
			"outcome":     "already_on_duty",
		}).Info("service call done")
		return "", fmt.Errorf("already on duty at store %s", de.CurrentStoreID)
	}

	storeID, err := s.qrService.ParseStoreID(qrCode)
	if err != nil {
		log.WithFields(logrus.Fields{
			"op":          "StartDuty",
			"duration_ms": time.Since(start).Milliseconds(),
			"outcome":     "invalid_qr",
		}).Info("service call done")
		return "", fmt.Errorf("invalid QR code: %w", err)
	}

	if err := s.qrService.ValidateQRCode(qrCode, storeID); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "StartDuty",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return "", err
	}

	if err := s.deRepo.UpdateStatus(ctx, dePhone, models.DEStatusEligible, storeID, ""); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"op":          "StartDuty",
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("service call failed")
		return "", fmt.Errorf("failed to update DE status: %w", err)
	}

	log.WithFields(logrus.Fields{
		"op":          "StartDuty",
		"duration_ms": time.Since(start).Milliseconds(),
		"de_phone":    dePhone,
		"store_id":    storeID,
	}).Info("service call done")
	return storeID, nil
}
