package models

const CallCapPerDirection = 10

// CallRecord persists per-call lifecycle data, keyed under the trip.
// PK = "TRIP!" + TripID, SK = "CALL!" + CallID.
type CallRecord struct {
	TripID      string `dynamodbav:"-"`
	CallID      string `dynamodbav:"call_id"`
	Direction   string `dynamodbav:"direction"` // "cust_to_de" | "de_to_cust"
	From        string `dynamodbav:"from"`
	To          string `dynamodbav:"to"`
	Status      string `dynamodbav:"status"`
	Answered    bool   `dynamodbav:"answered"`
	StartedAt   string `dynamodbav:"started_at"`
	EndedAt     string `dynamodbav:"ended_at,omitempty"`
	DurationSec int    `dynamodbav:"duration_sec"`
}

func (r *CallRecord) GetPK() string { return "TRIP!" + r.TripID }
func (r *CallRecord) GetSK() string { return "CALL!" + r.CallID }
