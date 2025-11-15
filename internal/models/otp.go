package models

import "time"

type OTPData struct {
	OTPHash   string    `json:"otp_hash"`
	Phone     string    `json:"phone"`
	Attempts  int       `json:"attempts"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}
