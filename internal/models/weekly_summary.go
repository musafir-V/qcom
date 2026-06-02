package models

// DEWeeklySummary records the weekly consistency bonus for a DE for one week.
// PK = WEEKLY!{deId}, SK = {weekStartDate}
type DEWeeklySummary struct {
	DEID           string  `json:"de_id" dynamodbav:"de_id"`
	WeekStartDate  string  `json:"week_start_date" dynamodbav:"week_start_date"`
	WeekEndDate    string  `json:"week_end_date" dynamodbav:"week_end_date"`
	DaysWorked     int     `json:"days_worked" dynamodbav:"days_worked"`
	TripsCompleted int     `json:"trips_completed" dynamodbav:"trips_completed"`
	BonusAmountZMW float64 `json:"bonus_amount_zmw" dynamodbav:"bonus_amount_zmw"`
	Status         string  `json:"status" dynamodbav:"status"`
	CreatedAt      string  `json:"created_at" dynamodbav:"created_at"`
}

func (w *DEWeeklySummary) GetPK() string { return "WEEKLY!" + w.DEID }
func (w *DEWeeklySummary) GetSK() string { return w.WeekStartDate }
