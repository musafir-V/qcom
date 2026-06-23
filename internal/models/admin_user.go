package models

// AdminUser is a username/password account that can sign in to the ops admin
// dashboard. Stored in the single table under PK="ADMIN_USER", SK=username.
type AdminUser struct {
	Username     string `json:"username" dynamodbav:"username"`
	Name         string `json:"name" dynamodbav:"name"`
	PasswordHash string `json:"-" dynamodbav:"password_hash"`
	Disabled     bool   `json:"disabled" dynamodbav:"disabled"`
	CreatedAt    string `json:"created_at" dynamodbav:"created_at"`
	UpdatedAt    string `json:"updated_at" dynamodbav:"updated_at"`
}

func (a *AdminUser) GetPK() string { return "ADMIN_USER" }

func (a *AdminUser) GetSK() string { return a.Username }
