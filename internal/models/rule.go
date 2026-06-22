package models

import (
	"encoding/json"
	"fmt"
)

type RuleFamily string

const (
	FamilyRateModifier RuleFamily = "rate_modifier"
	FamilyAccumulator  RuleFamily = "accumulator"
	FamilyRanking      RuleFamily = "ranking"
)

type Rule struct {
	ID            string          `json:"id" dynamodbav:"id"`
	Name          string          `json:"name" dynamodbav:"name"`
	Family        RuleFamily      `json:"family" dynamodbav:"family"`
	Enabled       bool            `json:"enabled" dynamodbav:"enabled"`
	EffectiveFrom *string         `json:"effective_from,omitempty" dynamodbav:"effective_from,omitempty"`
	EffectiveTo   *string         `json:"effective_to,omitempty" dynamodbav:"effective_to,omitempty"`
	Priority      int             `json:"priority" dynamodbav:"priority"`
	Version       int             `json:"version" dynamodbav:"version"`
	Spec          json.RawMessage `json:"spec" dynamodbav:"spec"`
}

func (r *Rule) GetPK() string { return "RULE" }

func (r *Rule) GetSK() string { return fmt.Sprintf("%s#%s#v%d", r.Family, r.ID, r.Version) }

type RateModifierSpec struct {
	DaysOfWeek []int   `json:"days_of_week"` // 0=Sun..6=Sat; empty = every day
	StartTime  string  `json:"start_time"`   // "17:30"; empty = all day
	EndTime    string  `json:"end_time"`     // "23:00"
	Multiplier float64 `json:"multiplier"`   // >=1.0 (default 1.0)
	FlatZMW    float64 `json:"flat_zmw"`     // >=0
}

type Reward struct {
	Kind      string  `json:"kind"`       // "cash" | "in_kind"
	AmountZMW float64 `json:"amount_zmw"` // cash
	Label     string  `json:"label"`      // in_kind display
	SKU       string  `json:"sku"`        // in_kind
}

type AccumulatorSpec struct {
	Metric        string  `json:"metric"`            // "on_time_trips"
	Window        string  `json:"window"`            // "daily" | "weekly"
	Threshold     int     `json:"threshold"`
	RequireNoFail bool    `json:"require_no_fail"`  // B1
	MinOnTimeRate float64 `json:"min_on_time_rate"` // e.g. 0.95 for B2/B3; 0 = ignore
	Reward        Reward  `json:"reward"`
}

type RankingSpec struct {
	Window       string  `json:"window"`        // "weekly"
	TopN         int     `json:"top_n"`
	MinOnTime    int     `json:"min_on_time"`   // eligibility floor
	WeightRate   float64 `json:"weight_rate"`   // default 0.5
	WeightVolume float64 `json:"weight_volume"` // default 0.5
	Reward       Reward  `json:"reward"`
}
