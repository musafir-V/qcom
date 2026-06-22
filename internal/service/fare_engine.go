package service

import (
	"encoding/json"
	"math"
	"time"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/money"
	"github.com/qcom/qcom/internal/timezone"
)

type RateDecision struct {
	RuleID     string
	Version    int
	Multiplier float64
	FlatZMW    float64
}

type FareEngine struct {
	ruleCache *RuleCache
}

func NewFareEngine(ruleCache *RuleCache) *FareEngine {
	return &FareEngine{ruleCache: ruleCache}
}

// ResolveRate returns the single best decision for a base fare at time `at`.
// No rule applies -> {Multiplier:1, Flat:0}.
func (e *FareEngine) ResolveRate(at time.Time, baseZMW float64) RateDecision {
	defaultDecision := RateDecision{Multiplier: 1, FlatZMW: 0}
	if e == nil || e.ruleCache == nil {
		return defaultDecision
	}

	atLocal := at.In(timezone.ZambiaLocation())
	best := defaultDecision
	bestPay := ApplyRate(baseZMW, best)
	bestPriority := math.MinInt
	hasWinner := false

	for _, rule := range e.ruleCache.ActiveRateModifiers(at) {
		if rule == nil {
			continue
		}
		spec, ok := parseRateModifierSpec(rule.Spec)
		if !ok || !matchesRateWindow(atLocal, spec) {
			continue
		}

		decision := RateDecision{
			RuleID:     rule.ID,
			Version:    rule.Version,
			Multiplier: normalizeMultiplier(spec.Multiplier),
			FlatZMW:    normalizeFlat(spec.FlatZMW),
		}
		pay := ApplyRate(baseZMW, decision)

		if !hasWinner ||
			pay > bestPay ||
			(pay == bestPay && (rule.Priority > bestPriority ||
				(rule.Priority == bestPriority && rule.ID < best.RuleID))) {
			best = decision
			bestPay = pay
			bestPriority = rule.Priority
			hasWinner = true
		}
	}

	return best
}

// Apply returns round2(base*mult + flat).
func ApplyRate(baseZMW float64, d RateDecision) float64 {
	mult := normalizeMultiplier(d.Multiplier)
	flat := normalizeFlat(d.FlatZMW)
	return money.Round2ZMW(baseZMW*mult + flat)
}

func parseRateModifierSpec(raw json.RawMessage) (models.RateModifierSpec, bool) {
	var spec models.RateModifierSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return models.RateModifierSpec{}, false
	}
	return spec, true
}

func matchesRateWindow(at time.Time, spec models.RateModifierSpec) bool {
	if len(spec.DaysOfWeek) > 0 {
		matchDay := false
		weekday := int(at.Weekday())
		for _, day := range spec.DaysOfWeek {
			if day == weekday {
				matchDay = true
				break
			}
		}
		if !matchDay {
			return false
		}
	}

	if spec.StartTime == "" && spec.EndTime == "" {
		return true
	}

	nowMin := at.Hour()*60 + at.Minute()
	startMin, okStart := parseHHMM(spec.StartTime)
	endMin, okEnd := parseHHMM(spec.EndTime)

	switch {
	case spec.StartTime != "" && !okStart:
		return false
	case spec.EndTime != "" && !okEnd:
		return false
	case spec.StartTime == "":
		return nowMin <= endMin
	case spec.EndTime == "":
		return nowMin >= startMin
	case startMin <= endMin:
		return nowMin >= startMin && nowMin <= endMin
	default:
		// Overnight window (e.g., 22:00-03:00).
		return nowMin >= startMin || nowMin <= endMin
	}
}

func parseHHMM(s string) (int, bool) {
	if s == "" {
		return 0, true
	}
	parsed, err := time.Parse("15:04", s)
	if err != nil {
		return 0, false
	}
	return parsed.Hour()*60 + parsed.Minute(), true
}

func normalizeMultiplier(multiplier float64) float64 {
	if multiplier < 1 {
		return 1
	}
	return multiplier
}

func normalizeFlat(flat float64) float64 {
	if flat < 0 {
		return 0
	}
	return flat
}
