package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

var (
	ErrInvalidDepositAmount = errors.New("amount_zmw must be positive")
	ErrDepositDENotFound    = errors.New("delivery executive not found")
	ErrNoCashInHand         = errors.New("no in-hand cash to deposit")
	// ErrDepositConflict surfaces an optimistic-lock / idempotent-replay
	// conflict from the deposit transaction (maps to HTTP 409).
	ErrDepositConflict = errors.New("cash deposit conflict")
)

// ValidateDepositAmount rejects non-positive deposit amounts.
func ValidateDepositAmount(amount float64) error {
	if amount <= 0 {
		return ErrInvalidDepositAmount
	}
	return nil
}

// ClampDeposit applies a requested deposit against current in-hand cash,
// flooring the new balance at zero. Returns (applied, newBalance).
func ClampDeposit(requested, inHand float64) (applied, newBalance float64) {
	applied = requested
	if applied > inHand {
		applied = inHand
	}
	return applied, inHand - applied
}

type CashDepositService struct {
	deRepo         *repository.DERepository
	cashConfigRepo *repository.CashConfigRepository
	logger         *logrus.Logger
}

func NewCashDepositService(deRepo *repository.DERepository, cashConfigRepo *repository.CashConfigRepository, logger *logrus.Logger) *CashDepositService {
	return &CashDepositService{deRepo: deRepo, cashConfigRepo: cashConfigRepo, logger: logger}
}

// DepositResult is returned to the ops caller — the one place in-hand cash is surfaced.
type DepositResult struct {
	NewBalanceZMW float64
	CashBlocked   bool
}

// RecordDeposit validates, clamps, and atomically decrements the DE's in-hand
// cash while appending an idempotent ledger entry. depositID may be empty; the
// repository generates one. Returns the new balance and whether the DE is still blocked.
func (s *CashDepositService) RecordDeposit(ctx context.Context, phone, depositID string, requested float64) (*DepositResult, error) {
	op := logging.Start(ctx, s.logger, "RecordDeposit", logrus.Fields{"phone": phone, "requested": requested})
	defer op.End()

	if err := ValidateDepositAmount(requested); err != nil {
		return nil, op.Outcome("invalid_amount", err)
	}

	de, err := s.deRepo.GetByPhone(ctx, phone)
	if err != nil {
		return nil, op.Fail(err)
	}
	if de == nil {
		return nil, op.Outcome("not_found", ErrDepositDENotFound)
	}

	cfg, err := s.cashConfigRepo.Get(ctx)
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to fetch cash config: %w", err))
	}

	applied, newBalance := ClampDeposit(requested, de.InHandCashZMW)

	if applied == 0 {
		return nil, op.Outcome("no_cash_in_hand", ErrNoCashInHand)
	}

	entry := &models.CashDepositLedger{
		DEID:               de.PhoneNumber,
		DepositID:          depositID,
		RequestedAmountZMW: requested,
		AppliedAmountZMW:   applied,
		CreatedAt:          time.Now().UTC().Format(time.RFC3339),
	}

	if err := s.deRepo.ApplyCashDeposit(ctx, de.PhoneNumber, de.InHandCashZMW, newBalance, entry); err != nil {
		if errors.Is(err, repository.ErrCashDepositConflict) {
			return nil, op.Outcome("conflict", ErrDepositConflict)
		}
		return nil, op.Fail(fmt.Errorf("failed to apply cash deposit: %w", err))
	}

	return &DepositResult{
		NewBalanceZMW: newBalance,
		CashBlocked:   newBalance > cfg.EffectiveLimitZMW(),
	}, nil
}
