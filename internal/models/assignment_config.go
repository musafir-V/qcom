package models

// DefaultAutoRejectTimeSeconds is the fallback accept window (1 minute) used
// when no AssignmentConfig row exists or the stored value is non-positive.
const DefaultAutoRejectTimeSeconds = 60

// AssignmentConfig holds operational knobs for trip assignment, stored as a
// singleton DynamoDB item (PK=CONFIG, SK=ASSIGNMENT_V1) so ops can tune it
// live without a redeploy.
type AssignmentConfig struct {
	AutoRejectTimeSeconds int `json:"auto_reject_time_seconds" dynamodbav:"auto_reject_time_seconds"`
}

func (c *AssignmentConfig) GetPK() string { return "CONFIG" }
func (c *AssignmentConfig) GetSK() string { return "ASSIGNMENT_V1" }

// EffectiveAutoRejectSeconds returns the configured window, or the default when
// unset/non-positive.
func (c *AssignmentConfig) EffectiveAutoRejectSeconds() int {
	if c.AutoRejectTimeSeconds <= 0 {
		return DefaultAutoRejectTimeSeconds
	}
	return c.AutoRejectTimeSeconds
}
