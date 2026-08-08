package models

// SMSOTPRoutingConfig is the admin kill-switch singleton for SMS OTP provider
// routing (PK=CONFIG, SK=SMS_OTP_ROUTING_V1).
// Default when missing: ForceTwilio=false (split mode — AT + Twilio).
type SMSOTPRoutingConfig struct {
	ForceTwilio bool `dynamodbav:"force_twilio" json:"force_twilio"`
}

func (c SMSOTPRoutingConfig) GetPK() string { return "CONFIG" }
func (c SMSOTPRoutingConfig) GetSK() string { return "SMS_OTP_ROUTING_V1" }
