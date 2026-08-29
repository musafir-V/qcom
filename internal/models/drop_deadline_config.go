package models

import "time"

const (
	DefaultDropDeadlineMinutesPerKm = 2.0
	DefaultDropDeadlineExtraMinutes = 0.0
)

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
	if mins < 0 {
		mins = 0
	}
	return now.Add(time.Duration(mins * float64(time.Minute))).Unix()
}
