package models

import (
	"time"
)

type User struct {
	PhoneNumber string    `json:"phone_number" dynamodbav:"phone_number"`
	Name        string    `json:"name,omitempty" dynamodbav:"name,omitempty"`
	CreatedAt   time.Time `json:"created_at" dynamodbav:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" dynamodbav:"updated_at"`
}

func (u *User) GetPK() string {
	return "USER!" + u.PhoneNumber
}

func (u *User) GetSK() string {
	return "METADATA"
}
