package service

import (
	"context"
	"fmt"

	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

type DEService struct {
	deRepo             *repository.DERepository
	qrService          *QRService
	referralService    *ReferralService
	earningsLedgerRepo deEarningsLedgerReader
	cashConfigRepo     *repository.CashConfigRepository
	logger             *logrus.Logger
}

type deEarningsLedgerReader interface {
	SumPositiveCashByDEAfter(ctx context.Context, deID, afterTimestamp string) (float64, error)
}

func NewDEService(deRepo *repository.DERepository, qrService *QRService, referralService *ReferralService, earningsLedgerRepo deEarningsLedgerReader, cashConfigRepo *repository.CashConfigRepository, logger *logrus.Logger) *DEService {
	return &DEService{deRepo: deRepo, qrService: qrService, referralService: referralService, earningsLedgerRepo: earningsLedgerRepo, cashConfigRepo: cashConfigRepo, logger: logger}
}

type RegisterDERequest struct {
	PhoneNumber      string
	Name             string
	ProfileURL       string
	NRCURL           string
	DriverLicenseURL string
	ReferralCode     string // optional — code of the DE that referred this one
}

func (s *DEService) Register(ctx context.Context, req RegisterDERequest) (*models.DeliveryExecutive, error) {
	op := logging.Start(ctx, s.logger, "Register", logrus.Fields{"phone": req.PhoneNumber})
	defer op.End()

	// Generate a unique referral code for this new DE
	referralCode, err := s.referralService.GenerateUniqueCode(ctx)
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to generate referral code: %w", err))
	}

	de := &models.DeliveryExecutive{
		PhoneNumber:      req.PhoneNumber,
		Name:             req.Name,
		ProfileURL:       req.ProfileURL,
		NRCURL:           req.NRCURL,
		DriverLicenseURL: req.DriverLicenseURL,
		Status:           models.DEStatusOffline,
		ReferralCode:     referralCode,
	}

	if err := s.deRepo.Create(ctx, de); err != nil {
		return nil, op.Fail(err)
	}

	// Link referral if a code was provided — non-fatal if invalid
	if req.ReferralCode != "" {
		if linkErr := s.referralService.LinkReferral(ctx, de.DEID, de.Name, req.ReferralCode); linkErr != nil {
			s.logger.WithError(linkErr).Warn("referral linking failed during registration — continuing")
		}
	}

	return de, nil
}

func (s *DEService) GetDE(ctx context.Context, phone string) (*models.DeliveryExecutive, error) {
	op := logging.Start(ctx, s.logger, "GetDE", logrus.Fields{"phone": phone})
	defer op.End()

	de, err := s.deRepo.GetByPhone(ctx, phone)
	if err != nil {
		return nil, op.Fail(err)
	}
	if de == nil {
		return nil, op.Outcome("not_found", fmt.Errorf("delivery executive not found"))
	}
	return de, nil
}

// GetTodayEarnings returns the sum of the DE's earnings ledger entries since
// midnight Zambia time.
func (s *DEService) GetTodayEarnings(ctx context.Context, deID string) (float64, error) {
	return s.earningsLedgerRepo.SumPositiveCashByDEAfter(ctx, deID, timezone.StartOfDayString())
}

// StartDuty validates the QR code and transitions the DE to eligible status.
// Valid from: offline or free.
func (s *DEService) StartDuty(ctx context.Context, dePhone, qrCode string) (string, error) {
	op := logging.Start(ctx, s.logger, "StartDuty", logrus.Fields{"phone": dePhone})
	defer op.End()

	de, err := s.deRepo.GetByPhone(ctx, dePhone)
	if err != nil {
		return "", op.Fail(fmt.Errorf("failed to fetch DE: %w", err))
	}
	if de == nil {
		return "", op.Outcome("not_found", fmt.Errorf("delivery executive not found"))
	}

	if de.Status == models.DEStatusBusy {
		return "", op.Outcome("busy", fmt.Errorf("cannot start duty while on an active delivery"))
	}
	if de.Status == models.DEStatusEligible {
		return "", op.Outcome("already_on_duty", fmt.Errorf("already on duty at store %s", de.CurrentStoreID))
	}

	cfg, err := s.cashConfigRepo.Get(ctx)
	if err != nil {
		return "", op.Fail(fmt.Errorf("failed to fetch cash config: %w", err))
	}
	if de.CashExceeds(cfg.EffectiveLimitZMW()) {
		return "", op.Outcome("cash_limit_exceeded", fmt.Errorf("in-hand cash limit exceeded; deposit cash to resume"))
	}

	storeID, err := s.qrService.ParseStoreID(qrCode)
	if err != nil {
		return "", op.Outcome("invalid_qr", fmt.Errorf("invalid QR code: %w", err))
	}

	if err := s.qrService.ValidateQRCode(qrCode, storeID); err != nil {
		return "", op.Fail(err)
	}

	if err := s.deRepo.UpdateStatus(ctx, dePhone, models.DEStatusEligible, storeID, ""); err != nil {
		return "", op.Fail(fmt.Errorf("failed to update DE status: %w", err))
	}

	op.With("store_id", storeID)
	return storeID, nil
}

// EndDuty transitions the DE from eligible or free to offline.
// Rejected if DE is busy (active trip in progress).
func (s *DEService) EndDuty(ctx context.Context, dePhone string) error {
	op := logging.Start(ctx, s.logger, "EndDuty", logrus.Fields{"phone": dePhone})
	defer op.End()

	de, err := s.deRepo.GetByPhone(ctx, dePhone)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to fetch DE: %w", err))
	}
	if de == nil {
		return op.Outcome("not_found", fmt.Errorf("delivery executive not found"))
	}
	if de.Status == models.DEStatusBusy {
		return op.Outcome("busy", fmt.Errorf("cannot end duty while on an active delivery"))
	}
	if de.Status == models.DEStatusOffline {
		return op.Outcome("already_offline", fmt.Errorf("already offline"))
	}

	if err := s.deRepo.UpdateStatus(ctx, dePhone, models.DEStatusOffline, "", ""); err != nil {
		return op.Fail(fmt.Errorf("failed to update DE status: %w", err))
	}
	return nil
}
