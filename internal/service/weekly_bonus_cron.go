package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/qcom/qcom/internal/money"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

const weeklyLockSK = "weekly-bonus"

type WeeklyBonusCron struct {
	deRepo             *repository.DERepository
	tripRepo           *repository.TripRepository
	weeklySummaryRepo  *repository.WeeklySummaryRepository
	earningsLedgerRepo *repository.EarningsLedgerRepository
	payoutConfigRepo   *repository.PayoutConfigRepository
	cronLockRepo       *repository.CronLockRepository
	logger             *logrus.Logger

	stopCh chan struct{}
}

func NewWeeklyBonusCron(
	deRepo *repository.DERepository,
	tripRepo *repository.TripRepository,
	weeklySummaryRepo *repository.WeeklySummaryRepository,
	earningsLedgerRepo *repository.EarningsLedgerRepository,
	payoutConfigRepo *repository.PayoutConfigRepository,
	cronLockRepo *repository.CronLockRepository,
	logger *logrus.Logger,
) *WeeklyBonusCron {
	return &WeeklyBonusCron{
		deRepo:             deRepo,
		tripRepo:           tripRepo,
		weeklySummaryRepo:  weeklySummaryRepo,
		earningsLedgerRepo: earningsLedgerRepo,
		payoutConfigRepo:   payoutConfigRepo,
		cronLockRepo:       cronLockRepo,
		logger:             logger,
		stopCh:             make(chan struct{}),
	}
}

func (c *WeeklyBonusCron) Start() {
	delay := c.durationUntilNextMondayMidnight()
	c.logger.WithField("delay_hours", delay.Hours()).
		Info("weekly bonus cron: scheduled for next Monday midnight Zambia time")
	go func() {
		select {
		case <-time.After(delay):
			c.runAndReschedule()
		case <-c.stopCh:
			return
		}
	}()
}

func (c *WeeklyBonusCron) Stop() { close(c.stopCh) }

func (c *WeeklyBonusCron) runAndReschedule() {
	c.run()
	go func() {
		select {
		case <-time.After(7 * 24 * time.Hour):
			c.runAndReschedule()
		case <-c.stopCh:
			return
		}
	}()
}

func (c *WeeklyBonusCron) run() {
	defer func() {
		if r := recover(); r != nil {
			c.logger.WithField("panic", r).Error("weekly bonus cron: panic recovered")
		}
	}()

	ctx := context.Background()
	c.logger.Info("weekly bonus cron: starting run")

	acquired, err := c.cronLockRepo.AcquireWithSK(ctx, weeklyLockSK, 3600)
	if err != nil {
		c.logger.WithError(err).Error("weekly bonus cron: failed to acquire lock")
		return
	}
	if !acquired {
		c.logger.Info("weekly bonus cron: lock held by another instance, skipping")
		return
	}
	defer func() { _ = c.cronLockRepo.ReleaseWithSK(ctx, weeklyLockSK) }()

	cfg, err := c.payoutConfigRepo.Get(ctx)
	if err != nil {
		c.logger.WithError(err).Error("weekly bonus cron: failed to get payout config")
		return
	}

	weekStart, weekEnd := c.previousWeekBounds()
	c.logger.WithFields(logrus.Fields{"week_start": weekStart, "week_end": weekEnd}).
		Info("weekly bonus cron: processing week")

	des, err := c.scanAllDEs(ctx)
	if err != nil {
		c.logger.WithError(err).Error("weekly bonus cron: failed to scan DEs")
		return
	}

	processed, skipped := 0, 0
	for _, de := range des {
		if err := c.processDE(ctx, de, weekStart, weekEnd, cfg); err != nil {
			c.logger.WithError(err).WithField("de_id", de.DEID).
				Warn("weekly bonus cron: failed to process DE, skipping")
			skipped++
			continue
		}
		processed++
	}

	c.logger.WithFields(logrus.Fields{"processed": processed, "skipped": skipped}).
		Info("weekly bonus cron: run complete")
}

func (c *WeeklyBonusCron) processDE(ctx context.Context, de *models.DeliveryExecutive, weekStart, weekEnd string, cfg *models.PayoutConfig) error {
	daysWorked, totalTrips, err := c.countWorkingDays(ctx, de.DEID, weekStart, weekEnd, cfg.MinDeliveriesPerDay)
	if err != nil {
		return fmt.Errorf("failed to count working days: %w", err)
	}

	bonusZMW := computeWeeklyBonus(daysWorked, cfg)
	if bonusZMW == 0 {
		return nil
	}

	summary := &models.DEWeeklySummary{
		DEID:           de.DEID,
		WeekStartDate:  weekStart,
		WeekEndDate:    weekEnd,
		DaysWorked:     daysWorked,
		TripsCompleted: totalTrips,
		BonusAmountZMW: bonusZMW,
		Status:         "computed",
	}
	if err := c.weeklySummaryRepo.Create(ctx, summary); err != nil {
		if isAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("failed to write weekly summary: %w", err)
	}

	entry := &models.EarningsLedger{
		DEID:        de.DEID,
		EarningID:   uuid.New().String(),
		Type:        models.EarningTypeWeeklyBonus,
		AmountZMW:   bonusZMW,
		CreatedAt:   timezone.Now().Format(time.RFC3339),
		ReferenceID: weekStart,
	}
	if err := c.earningsLedgerRepo.Append(ctx, entry); err != nil {
		return fmt.Errorf("failed to write ledger entry: %w", err)
	}

	c.logger.WithFields(logrus.Fields{
		"de_id": de.DEID, "days_worked": daysWorked, "bonus_zmw": bonusZMW,
	}).Info("weekly bonus cron: bonus credited")
	return nil
}

func (c *WeeklyBonusCron) countWorkingDays(ctx context.Context, deID, weekStart, weekEnd string, minDeliveries int) (int, int, error) {
	trips, _, err := c.tripRepo.ListByDEAfter(ctx, deID, weekStart+"T00:00:00+02:00", 200, nil)
	if err != nil {
		return 0, 0, err
	}

	dayCount := make(map[string]int)
	total := 0
	for _, trip := range trips {
		if trip.CompletedAt == "" || trip.CompletedAt > weekEnd+"T23:59:59+02:00" {
			continue
		}
		t, err := time.Parse(time.RFC3339, trip.CompletedAt)
		if err != nil {
			continue
		}
		date := t.In(timezone.ZambiaLocation()).Format("2006-01-02")
		dayCount[date]++
		total++
	}

	daysWorked := 0
	for _, count := range dayCount {
		if count >= minDeliveries {
			daysWorked++
		}
	}
	return daysWorked, total, nil
}

func computeWeeklyBonus(daysWorked int, cfg *models.PayoutConfig) float64 {
	switch {
	case daysWorked >= cfg.WeeklyW3Days:
		return money.RoundUpZMW(cfg.WeeklyW3BonusZMW)
	case daysWorked >= cfg.WeeklyW2Days:
		return money.RoundUpZMW(cfg.WeeklyW2BonusZMW)
	case daysWorked >= cfg.WeeklyW1Days:
		return money.RoundUpZMW(cfg.WeeklyW1BonusZMW)
	default:
		return 0
	}
}

func (c *WeeklyBonusCron) previousWeekBounds() (string, string) {
	now := timezone.Now()
	lastWeek := now.AddDate(0, 0, -7)
	weekday := int(lastWeek.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	monday := lastWeek.AddDate(0, 0, -(weekday - 1))
	sunday := monday.AddDate(0, 0, 6)
	return monday.Format("2006-01-02"), sunday.Format("2006-01-02")
}

func (c *WeeklyBonusCron) durationUntilNextMondayMidnight() time.Duration {
	loc := timezone.ZambiaLocation()
	now := time.Now().In(loc)
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	daysUntilMonday := (8 - weekday) % 7
	if daysUntilMonday == 0 {
		daysUntilMonday = 7
	}
	nextMonday := time.Date(now.Year(), now.Month(), now.Day()+daysUntilMonday, 0, 0, 0, 0, loc)
	return time.Until(nextMonday)
}

func (c *WeeklyBonusCron) scanAllDEs(ctx context.Context) ([]*models.DeliveryExecutive, error) {
	return c.deRepo.ScanAll(ctx)
}

func isAlreadyExists(err error) bool {
	return err != nil && strings.Contains(err.Error(), "already exists")
}
