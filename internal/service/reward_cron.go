package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/qcom/qcom/internal/timezone"
	"github.com/sirupsen/logrus"
)

const (
	rewardDailyLockSK      = "reward-daily"
	rewardWeeklyLockSK     = "reward-weekly"
	rewardLockTTLSec   int = 3600
)

type rewardDERepo interface {
	ScanAll(ctx context.Context) ([]*models.DeliveryExecutive, error)
}

type rewardTripRepo interface {
	ListByDEWindow(
		ctx context.Context,
		deID, startTimestamp, endTimestamp string,
		pageSize int32,
		lastKey map[string]types.AttributeValue,
	) ([]*models.Trip, map[string]types.AttributeValue, error)
}

type rewardRuleRepo interface {
	ListAll(ctx context.Context) ([]*models.Rule, error)
}

type rewardLedgerRepo interface {
	ExistsByReference(ctx context.Context, deID string, earningType models.EarningType, referenceID string) (bool, error)
	Append(ctx context.Context, entry *models.EarningsLedger) error
}

type rewardLockRepo interface {
	AcquireWithSK(ctx context.Context, sk string, ttlSeconds int) (bool, error)
	ReleaseWithSK(ctx context.Context, sk string) error
}

type RewardCron struct {
	deRepo     rewardDERepo
	tripRepo   rewardTripRepo
	ruleRepo   rewardRuleRepo
	ledgerRepo rewardLedgerRepo
	lockRepo   rewardLockRepo
	logger     *logrus.Logger
}

func NewRewardCron(
	deRepo *repository.DERepository,
	tripRepo *repository.TripRepository,
	ruleRepo *repository.RuleRepository,
	ledgerRepo *repository.EarningsLedgerRepository,
	lockRepo *repository.CronLockRepository,
	logger *logrus.Logger,
) *RewardCron {
	return newRewardCronWithDeps(deRepo, tripRepo, ruleRepo, ledgerRepo, lockRepo, logger)
}

func newRewardCronWithDeps(
	deRepo rewardDERepo,
	tripRepo rewardTripRepo,
	ruleRepo rewardRuleRepo,
	ledgerRepo rewardLedgerRepo,
	lockRepo rewardLockRepo,
	logger *logrus.Logger,
) *RewardCron {
	if logger == nil {
		logger = logrus.New()
	}
	return &RewardCron{
		deRepo:     deRepo,
		tripRepo:   tripRepo,
		ruleRepo:   ruleRepo,
		ledgerRepo: ledgerRepo,
		lockRepo:   lockRepo,
		logger:     logger,
	}
}

// Start launches daily + weekly reward runners.
// Daily executes at Zambia midnight for the previous day.
// Weekly executes Monday midnight for the previous Mon-Sun window.
func (c *RewardCron) Start(ctx context.Context) {
	go c.runDailyLoop(ctx)
	go c.runWeeklyLoop(ctx)
}

func (c *RewardCron) runDailyLoop(ctx context.Context) {
	for {
		delay := durationUntilNextMidnight()
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
			c.runDaily(ctx)
		}
	}
}

func (c *RewardCron) runWeeklyLoop(ctx context.Context) {
	for {
		delay := durationUntilNextMondayMidnight()
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
			c.runWeekly(ctx)
		}
	}
}

func (c *RewardCron) runDaily(ctx context.Context) {
	defer c.recoverPanic("reward cron daily")

	acquired, err := c.lockRepo.AcquireWithSK(ctx, rewardDailyLockSK, rewardLockTTLSec)
	if err != nil {
		c.logger.WithError(err).Error("reward cron daily: failed to acquire lock")
		return
	}
	if !acquired {
		return
	}
	defer func() { _ = c.lockRepo.ReleaseWithSK(ctx, rewardDailyLockSK) }()

	targetDay := timezone.Now().AddDate(0, 0, -1)
	c.runDailyWindow(ctx, targetDay)
}

func (c *RewardCron) runWeekly(ctx context.Context) {
	defer c.recoverPanic("reward cron weekly")

	acquired, err := c.lockRepo.AcquireWithSK(ctx, rewardWeeklyLockSK, rewardLockTTLSec)
	if err != nil {
		c.logger.WithError(err).Error("reward cron weekly: failed to acquire lock")
		return
	}
	if !acquired {
		return
	}
	defer func() { _ = c.lockRepo.ReleaseWithSK(ctx, rewardWeeklyLockSK) }()

	weekStart := previousWeekMonday(timezone.Now())
	c.runWeeklyWindow(ctx, weekStart)
}

func (c *RewardCron) runDailyWindow(ctx context.Context, day time.Time) {
	loc := timezone.ZambiaLocation()
	day = day.In(loc)
	window := day.Format("2006-01-02")
	start, end := dayBounds(day)
	// Trips persist completed_at/cancelled_at as UTC RFC3339 ("...Z") in every
	// write path (TripRepository.UpdateStatus / CompleteTripAndFreeDE /
	// CancelByOrderID). The window BETWEEN filter compares raw strings, so the
	// bounds must be emitted in the SAME UTC format or a ~2h offset skew would
	// misattribute trips across the Zambia day boundary.
	startTS, endTS := start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339)

	specs := c.activeAccumulatorSpecs(ctx, "daily", end)
	if len(specs) == 0 {
		return
	}

	des, err := c.deRepo.ScanAll(ctx)
	if err != nil {
		c.logger.WithError(err).Error("reward cron daily: failed to scan DEs")
		return
	}

	for _, de := range des {
		trips, err := c.fetchWindowTrips(ctx, de.DEID, startTS, endTS)
		if err != nil {
			c.logger.WithError(err).WithField("de_id", de.DEID).Warn("reward cron daily: failed to fetch trips")
			continue
		}
		for _, spec := range specs {
			entries := EvaluateAccumulator(de.DEID, spec, window, trips)
			c.appendIdempotent(ctx, entries)
		}
	}
}

func (c *RewardCron) runWeeklyWindow(ctx context.Context, weekStart time.Time) {
	loc := timezone.ZambiaLocation()
	weekStart = time.Date(weekStart.In(loc).Year(), weekStart.In(loc).Month(), weekStart.In(loc).Day(), 0, 0, 0, 0, loc)
	weekEnd := weekStart.AddDate(0, 0, 6)
	window := isoWeekWindowKey(weekStart)
	start, _ := dayBounds(weekStart)
	_, end := dayBounds(weekEnd)
	// See runDailyWindow: bounds must match the stored UTC timestamp format.
	startTS, endTS := start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339)

	accSpecs := c.activeAccumulatorSpecs(ctx, "weekly", end)
	rankSpecs := c.activeRankingSpecs(ctx, "weekly", end)
	if len(accSpecs) == 0 && len(rankSpecs) == 0 {
		return
	}

	des, err := c.deRepo.ScanAll(ctx)
	if err != nil {
		c.logger.WithError(err).Error("reward cron weekly: failed to scan DEs")
		return
	}

	perDE := make(map[string][]*models.Trip, len(des))
	for _, de := range des {
		trips, err := c.fetchWindowTrips(ctx, de.DEID, startTS, endTS)
		if err != nil {
			c.logger.WithError(err).WithField("de_id", de.DEID).Warn("reward cron weekly: failed to fetch trips")
			continue
		}
		perDE[de.DEID] = trips
	}

	for deID, trips := range perDE {
		for _, spec := range accSpecs {
			entries := EvaluateAccumulator(deID, spec, window, trips)
			c.appendIdempotent(ctx, entries)
		}
	}
	for _, spec := range rankSpecs {
		entries := EvaluateRanking(spec, window, perDE)
		c.appendIdempotent(ctx, entries)
	}
}

func (c *RewardCron) appendIdempotent(ctx context.Context, entries []*models.EarningsLedger) {
	for _, entry := range entries {
		exists, err := c.ledgerRepo.ExistsByReference(ctx, entry.DEID, entry.Type, entry.ReferenceID)
		if err != nil {
			c.logger.WithError(err).WithFields(logrus.Fields{
				"de_id": entry.DEID, "type": entry.Type, "window": entry.ReferenceID,
			}).Warn("reward cron: idempotency check failed")
			continue
		}
		if exists {
			continue
		}
		if err := c.ledgerRepo.Append(ctx, entry); err != nil {
			c.logger.WithError(err).WithFields(logrus.Fields{
				"de_id": entry.DEID, "type": entry.Type, "window": entry.ReferenceID,
			}).Warn("reward cron: append failed")
		}
	}
}

func (c *RewardCron) fetchWindowTrips(ctx context.Context, deID, start, end string) ([]*models.Trip, error) {
	var all []*models.Trip
	var lastKey map[string]types.AttributeValue
	for {
		chunk, nextKey, err := c.tripRepo.ListByDEWindow(ctx, deID, start, end, 200, lastKey)
		if err != nil {
			return nil, err
		}
		all = append(all, chunk...)
		if nextKey == nil {
			return all, nil
		}
		lastKey = nextKey
	}
}

func (c *RewardCron) activeAccumulatorSpecs(ctx context.Context, window string, at time.Time) []models.AccumulatorSpec {
	rules, err := c.activeRewardRules(ctx, models.FamilyAccumulator, at)
	if err != nil {
		c.logger.WithError(err).Warn("reward cron: failed to load accumulator rules")
		return nil
	}
	specs := make([]models.AccumulatorSpec, 0, len(rules))
	for _, rule := range rules {
		var spec models.AccumulatorSpec
		if err := json.Unmarshal(rule.Spec, &spec); err != nil {
			c.logger.WithError(err).WithField("rule_id", rule.ID).Warn("reward cron: invalid accumulator spec")
			continue
		}
		if spec.Window == window {
			specs = append(specs, spec)
		}
	}
	return specs
}

func (c *RewardCron) activeRankingSpecs(ctx context.Context, window string, at time.Time) []models.RankingSpec {
	rules, err := c.activeRewardRules(ctx, models.FamilyRanking, at)
	if err != nil {
		c.logger.WithError(err).Warn("reward cron: failed to load ranking rules")
		return nil
	}
	specs := make([]models.RankingSpec, 0, len(rules))
	for _, rule := range rules {
		var spec models.RankingSpec
		if err := json.Unmarshal(rule.Spec, &spec); err != nil {
			c.logger.WithError(err).WithField("rule_id", rule.ID).Warn("reward cron: invalid ranking spec")
			continue
		}
		if spec.Window == window {
			specs = append(specs, spec)
		}
	}
	return specs
}

func (c *RewardCron) activeRewardRules(ctx context.Context, family models.RuleFamily, at time.Time) ([]*models.Rule, error) {
	rules, err := c.ruleRepo.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list rules: %w", err)
	}
	latestByID := make(map[string]*models.Rule)
	for _, rule := range rules {
		if rule == nil || rule.Family != family {
			continue
		}
		current, ok := latestByID[rule.ID]
		if !ok || rule.Version > current.Version {
			latestByID[rule.ID] = rule
		}
	}
	active := make([]*models.Rule, 0, len(latestByID))
	for _, rule := range latestByID {
		inRange, err := isRuleEffectiveAt(rule, at)
		if err != nil {
			c.logger.WithError(err).WithField("rule_id", rule.ID).Warn("reward cron: invalid rule effective window")
			continue
		}
		if rule.Enabled && inRange {
			active = append(active, rule)
		}
	}
	return active, nil
}

func (c *RewardCron) recoverPanic(label string) {
	if r := recover(); r != nil {
		c.logger.WithField("panic", r).Error(label + ": panic recovered")
	}
}

func durationUntilNextMidnight() time.Duration {
	loc := timezone.ZambiaLocation()
	now := time.Now().In(loc)
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, loc)
	return time.Until(next)
}

func durationUntilNextMondayMidnight() time.Duration {
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

func previousWeekMonday(now time.Time) time.Time {
	loc := timezone.ZambiaLocation()
	now = now.In(loc).AddDate(0, 0, -7)
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	return now.AddDate(0, 0, -(weekday - 1))
}

func dayBounds(day time.Time) (start, end time.Time) {
	loc := timezone.ZambiaLocation()
	day = day.In(loc)
	start = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	end = time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, 0, loc)
	return start, end
}

func isoWeekWindowKey(weekStart time.Time) string {
	year, week := weekStart.ISOWeek()
	return fmt.Sprintf("%04d-W%02d", year, week)
}
