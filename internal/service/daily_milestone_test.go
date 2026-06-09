package service

import (
	"testing"

	"github.com/qcom/qcom/internal/models"
)

func cfg(t1, t2 int, tmpl string) *models.PayoutConfig {
	return &models.PayoutConfig{
		Tier1Threshold:           t1,
		Tier2Threshold:           t2,
		MilestoneMessageTemplate: tmpl,
	}
}

const defaultTmpl = "Complete {remaining} more deliveries to unlock your next milestone"

func TestComputeDailyMilestone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		deliveriesToday int
		cfg             *models.PayoutConfig
		wantDeliveries  int
		wantBarMax      int
		wantThresholds  []int
		wantMessage     string
		wantTopTier     bool
	}{
		{
			name:            "below tier1 — zero deliveries",
			deliveriesToday: 0,
			cfg:             cfg(10, 15, defaultTmpl),
			wantDeliveries:  0,
			wantBarMax:      16,
			wantThresholds:  []int{10, 15},
			wantMessage:     "Complete 10 more deliveries to unlock your next milestone",
			wantTopTier:     false,
		},
		{
			name:            "below tier1 — some deliveries",
			deliveriesToday: 6,
			cfg:             cfg(10, 15, defaultTmpl),
			wantDeliveries:  6,
			wantBarMax:      16,
			wantThresholds:  []int{10, 15},
			wantMessage:     "Complete 4 more deliveries to unlock your next milestone",
			wantTopTier:     false,
		},
		{
			name:            "exactly at tier1",
			deliveriesToday: 10,
			cfg:             cfg(10, 15, defaultTmpl),
			wantDeliveries:  10,
			wantBarMax:      16,
			wantThresholds:  []int{10, 15},
			wantMessage:     "Complete 5 more deliveries to unlock your next milestone",
			wantTopTier:     false,
		},
		{
			name:            "between tier1 and tier2",
			deliveriesToday: 12,
			cfg:             cfg(10, 15, defaultTmpl),
			wantDeliveries:  12,
			wantBarMax:      16,
			wantThresholds:  []int{10, 15},
			wantMessage:     "Complete 3 more deliveries to unlock your next milestone",
			wantTopTier:     false,
		},
		{
			name:            "exactly at tier2",
			deliveriesToday: 15,
			cfg:             cfg(10, 15, defaultTmpl),
			wantDeliveries:  15,
			wantBarMax:      16,
			wantThresholds:  []int{10, 15},
			wantMessage:     "",
			wantTopTier:     true,
		},
		{
			name:            "above tier2",
			deliveriesToday: 20,
			cfg:             cfg(10, 15, defaultTmpl),
			wantDeliveries:  20,
			wantBarMax:      16,
			wantThresholds:  []int{10, 15},
			wantMessage:     "",
			wantTopTier:     true,
		},
		{
			name:            "empty template — below tier1",
			deliveriesToday: 3,
			cfg:             cfg(10, 15, ""),
			wantDeliveries:  3,
			wantBarMax:      16,
			wantThresholds:  []int{10, 15},
			wantMessage:     "",
			wantTopTier:     false,
		},
		{
			name:            "empty template — between tiers",
			deliveriesToday: 11,
			cfg:             cfg(10, 15, ""),
			wantDeliveries:  11,
			wantBarMax:      16,
			wantThresholds:  []int{10, 15},
			wantMessage:     "",
			wantTopTier:     false,
		},
		{
			name:            "remaining substitution — 1 left to tier1",
			deliveriesToday: 9,
			cfg:             cfg(10, 15, defaultTmpl),
			wantDeliveries:  9,
			wantBarMax:      16,
			wantThresholds:  []int{10, 15},
			wantMessage:     "Complete 1 more deliveries to unlock your next milestone",
			wantTopTier:     false,
		},
		{
			name:            "different thresholds — uses config values not hardcoded",
			deliveriesToday: 5,
			cfg:             cfg(8, 12, "Deliver {remaining} more to level up"),
			wantDeliveries:  5,
			wantBarMax:      13,
			wantThresholds:  []int{8, 12},
			wantMessage:     "Deliver 3 more to level up",
			wantTopTier:     false,
		},
		// Misconfig edge cases — function must not panic; values document current behavior.
		{
			name:            "misconfig — thresholds reversed (tier1>tier2)",
			deliveriesToday: 8,
			cfg:             cfg(15, 10, defaultTmpl),
			wantDeliveries:  8,
			wantBarMax:      11,
			wantThresholds:  []int{15, 10},
			wantMessage:     "Complete 7 more deliveries to unlock your next milestone",
			wantTopTier:     false,
		},
		{
			name:            "misconfig — zero thresholds",
			deliveriesToday: 3,
			cfg:             cfg(0, 0, defaultTmpl),
			wantDeliveries:  3,
			wantBarMax:      1,
			wantThresholds:  []int{0, 0},
			wantMessage:     "",
			wantTopTier:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ComputeDailyMilestone(tt.deliveriesToday, tt.cfg)

			if got.DeliveriesToday != tt.wantDeliveries {
				t.Errorf("DeliveriesToday: got %d, want %d", got.DeliveriesToday, tt.wantDeliveries)
			}
			if got.BarMax != tt.wantBarMax {
				t.Errorf("BarMax: got %d, want %d", got.BarMax, tt.wantBarMax)
			}
			if len(got.Thresholds) != len(tt.wantThresholds) {
				t.Errorf("Thresholds length: got %v, want %v", got.Thresholds, tt.wantThresholds)
			} else {
				for i, th := range tt.wantThresholds {
					if got.Thresholds[i] != th {
						t.Errorf("Thresholds[%d]: got %d, want %d", i, got.Thresholds[i], th)
					}
				}
			}
			if got.Message != tt.wantMessage {
				t.Errorf("Message: got %q, want %q", got.Message, tt.wantMessage)
			}
			if got.TopTierReached != tt.wantTopTier {
				t.Errorf("TopTierReached: got %v, want %v", got.TopTierReached, tt.wantTopTier)
			}
		})
	}
}
