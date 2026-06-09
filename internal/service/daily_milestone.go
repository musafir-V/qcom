package service

import (
	"fmt"
	"strings"

	"github.com/qcom/qcom/internal/models"
)

// DailyMilestone describes the driver's progress toward the daily tier bonuses.
// It is embedded in the GET /de/me response under the "daily_milestone" key.
type DailyMilestone struct {
	DeliveriesToday int    `json:"deliveries_today"`
	BarMax          int    `json:"bar_max"`
	Thresholds      []int  `json:"thresholds"`
	Message         string `json:"message"`
	TopTierReached  bool   `json:"top_tier_reached"`
}

// ComputeDailyMilestone is a pure function (no I/O) that builds the milestone
// state for the driver home card. Uses config values exclusively — no hardcoded
// thresholds.
func ComputeDailyMilestone(deliveriesToday int, cfg *models.PayoutConfig) DailyMilestone {
	m := DailyMilestone{
		DeliveriesToday: deliveriesToday,
		BarMax:          cfg.Tier2Threshold + 1,
		Thresholds:      []int{cfg.Tier1Threshold, cfg.Tier2Threshold},
	}

	if deliveriesToday >= cfg.Tier2Threshold {
		m.TopTierReached = true
		m.Message = ""
		return m
	}

	// Not at top tier yet — determine next milestone and build message.
	var next int
	if deliveriesToday < cfg.Tier1Threshold {
		next = cfg.Tier1Threshold
	} else {
		next = cfg.Tier2Threshold
	}

	if cfg.MilestoneMessageTemplate != "" {
		remaining := next - deliveriesToday
		m.Message = strings.ReplaceAll(cfg.MilestoneMessageTemplate, "{remaining}", fmt.Sprintf("%d", remaining))
	}

	return m
}
