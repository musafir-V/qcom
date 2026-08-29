package models

import (
	"math"
	"time"
)

const (
	DefaultDropDeadlineMinutesPerKm = 2.0
	DefaultDropDeadlineExtraMinutes = 0.0
)

// maxDropDeadlineMinutes is the exclusive upper bound: mins * Minute at this
// value is 2^63 and time.Duration wraps negative. Accepted values must be below it.
var maxDropDeadlineMinutes = float64(math.MaxInt64) / float64(time.Minute)

// MinutesFitDropDeadlineDuration reports whether mins can be converted to
// time.Duration without overflow, NaN, or Inf. extra_minutes=0 is valid.
func MinutesFitDropDeadlineDuration(mins float64) bool {
	if math.IsNaN(mins) || math.IsInf(mins, 0) || mins < 0 {
		return false
	}
	return mins < maxDropDeadlineMinutes
}

type DropDeadlineConfig struct {
	MinutesPerKm float64 `json:"minutes_per_km" dynamodbav:"minutes_per_km"`
	ExtraMinutes float64 `json:"extra_minutes" dynamodbav:"extra_minutes"`
}

func (c *DropDeadlineConfig) GetPK() string { return "CONFIG" }
func (c *DropDeadlineConfig) GetSK() string { return "DROP_DEADLINE_V1" }

func (c *DropDeadlineConfig) EffectiveMinutesPerKm() float64 {
	if c == nil || c.MinutesPerKm <= 0 {
		return DefaultDropDeadlineMinutesPerKm
	}
	return c.MinutesPerKm
}

func (c *DropDeadlineConfig) EffectiveExtraMinutes() float64 {
	if c == nil || c.ExtraMinutes < 0 {
		return DefaultDropDeadlineExtraMinutes
	}
	return c.ExtraMinutes
}

func ComputeDropDeadlineUnix(now time.Time, distanceKM, minutesPerKm, extraMinutes float64) int64 {
	mins := distanceKM*minutesPerKm + extraMinutes
	if mins < 0 || math.IsNaN(mins) {
		mins = 0
	}
	ns := mins * float64(time.Minute)
	if math.IsInf(ns, 0) || ns >= float64(math.MaxInt64) {
		return now.Add(time.Duration(math.MaxInt64)).Unix()
	}
	return now.Add(time.Duration(ns)).Unix()
}
