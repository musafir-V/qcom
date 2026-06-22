package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

type ruleCacheRepo interface {
	ListAll(ctx context.Context) ([]*models.Rule, error)
}

type RuleCache struct {
	repo   ruleCacheRepo
	ttl    time.Duration
	logger *logrus.Logger

	mu       sync.RWMutex
	snapshot []*models.Rule
}

func NewRuleCache(repo *repository.RuleRepository, ttl time.Duration, logger *logrus.Logger) *RuleCache {
	return newRuleCacheWithRepo(repo, ttl, logger)
}

func newRuleCacheWithRepo(repo ruleCacheRepo, ttl time.Duration, logger *logrus.Logger) *RuleCache {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &RuleCache{
		repo:   repo,
		ttl:    ttl,
		logger: logger,
	}
}

// Start launches a background refresh loop until ctx is cancelled.
func (c *RuleCache) Start(ctx context.Context) {
	if err := c.refresh(ctx); err != nil {
		c.logger.WithError(err).Warn("rule cache: initial refresh failed")
	}

	ticker := time.NewTicker(c.ttl)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				c.logger.Info("rule cache: stopped")
				return
			case <-ticker.C:
				if err := c.refresh(ctx); err != nil {
					c.logger.WithError(err).Warn("rule cache: refresh failed")
				}
			}
		}
	}()
}

func (c *RuleCache) ActiveRateModifiers(at time.Time) []*models.Rule {
	c.mu.RLock()
	snapshot := make([]*models.Rule, len(c.snapshot))
	copy(snapshot, c.snapshot)
	c.mu.RUnlock()

	latestByID := make(map[string]*models.Rule)
	for _, rule := range snapshot {
		if rule == nil || rule.Family != models.FamilyRateModifier {
			continue
		}
		existing, ok := latestByID[rule.ID]
		if !ok || rule.Version > existing.Version {
			latestByID[rule.ID] = rule
		}
	}

	active := make([]*models.Rule, 0, len(latestByID))
	for _, rule := range latestByID {
		inRange, err := isRuleEffectiveAt(rule, at)
		if err != nil {
			c.logger.WithError(err).WithFields(logrus.Fields{
				"rule_id": rule.ID, "version": rule.Version,
			}).Warn("rule cache: invalid effective range")
			continue
		}
		if rule.Enabled && inRange {
			active = append(active, rule)
		}
	}
	return active
}

func (c *RuleCache) refresh(ctx context.Context) error {
	rules, err := c.repo.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("list rules: %w", err)
	}
	c.mu.Lock()
	c.snapshot = rules
	c.mu.Unlock()

	c.logger.WithField("count", len(rules)).Debug("rule cache: snapshot refreshed")
	return nil
}

func isRuleEffectiveAt(rule *models.Rule, at time.Time) (bool, error) {
	if rule.EffectiveFrom != nil && *rule.EffectiveFrom != "" {
		from, err := time.Parse(time.RFC3339, *rule.EffectiveFrom)
		if err != nil {
			return false, fmt.Errorf("parse effective_from: %w", err)
		}
		if at.Before(from) {
			return false, nil
		}
	}
	if rule.EffectiveTo != nil && *rule.EffectiveTo != "" {
		to, err := time.Parse(time.RFC3339, *rule.EffectiveTo)
		if err != nil {
			return false, fmt.Errorf("parse effective_to: %w", err)
		}
		if at.After(to) {
			return false, nil
		}
	}
	return true, nil
}
