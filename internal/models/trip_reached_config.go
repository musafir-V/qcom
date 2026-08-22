package models

const DefaultReachedRadiusMeters = 150.0

// TripReachedConfig is PK=CONFIG SK=TRIP_REACHED_V1.
type TripReachedConfig struct {
	RadiusMeters                 float64 `json:"radius_meters" dynamodbav:"radius_meters"`
	RequireReachedBeforeComplete bool    `json:"require_reached_before_complete" dynamodbav:"require_reached_before_complete"`
}

func (c *TripReachedConfig) GetPK() string { return "CONFIG" }
func (c *TripReachedConfig) GetSK() string { return "TRIP_REACHED_V1" }

func (c *TripReachedConfig) EffectiveRadiusMeters() float64 {
	if c == nil || c.RadiusMeters <= 0 {
		return DefaultReachedRadiusMeters
	}
	return c.RadiusMeters
}

func (c *TripReachedConfig) RequireReached() bool {
	if c == nil {
		return false
	}
	return c.RequireReachedBeforeComplete
}
