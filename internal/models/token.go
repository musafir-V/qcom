package models

import "time"

type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

type RefreshTokenData struct {
	JTI        string    `json:"jti"`
	EntityID   string    `json:"entity_id"`
	EntityType string    `json:"entity_type"`
	Phone      string    `json:"phone"`
	FamilyID   string    `json:"family_id"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Revoked    bool      `json:"revoked"`
}

// RefreshReplacement caches the token pair issued when old_jti was rotated.
// Concurrent refreshes that lose the race read this within the reuse grace window
// and receive the same new tokens instead of TOKEN_REVOKED.
type RefreshReplacement struct {
	OldJTI       string    `json:"old_jti"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	ExpiresIn    int64     `json:"expires_in"`
	FamilyID     string    `json:"family_id"`
	NewJTI       string    `json:"new_jti"`
	IssuedAt     time.Time `json:"issued_at"`
}

func (r RefreshReplacement) TokenPair() TokenPair {
	return TokenPair{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		TokenType:    r.TokenType,
		ExpiresIn:    r.ExpiresIn,
	}
}
