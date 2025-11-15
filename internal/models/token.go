package models

import "time"

type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

type RefreshTokenData struct {
	JTI       string    `json:"jti"`
	UserID    string    `json:"user_id"`
	Phone     string    `json:"phone"`
	FamilyID  string    `json:"family_id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Revoked   bool      `json:"revoked"`
}
