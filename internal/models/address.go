package models

import "math"

type Address struct {
	AddressID        string  `json:"address_id" dynamodbav:"address_id"`
	UserID           string  `json:"user_id" dynamodbav:"user_id"`
	ReceiverName     string  `json:"receiver_name" dynamodbav:"receiver_name"`
	ReceiverPhone    string  `json:"receiver_phone" dynamodbav:"receiver_phone"`
	BuildingAndFloor string  `json:"building_and_floor" dynamodbav:"building_and_floor"`
	AddressLine1     string  `json:"address_line_1" dynamodbav:"address_line_1"`
	AddressLine2     string  `json:"address_line_2,omitempty" dynamodbav:"address_line_2,omitempty"`
	Latitude         float64 `json:"latitude" dynamodbav:"latitude"`
	Longitude        float64 `json:"longitude" dynamodbav:"longitude"`
	Tag              string  `json:"tag,omitempty" dynamodbav:"label,omitempty"`
	IsActive         bool    `json:"is_active" dynamodbav:"is_active"`
	CreatedAt        string  `json:"created_at" dynamodbav:"created_at"`
	UpdatedAt        string  `json:"updated_at" dynamodbav:"updated_at"`
}

type SuggestedAddress struct {
	Address
	DistanceMeters float64 `json:"distance_meters"`
}

func (a *Address) GetPK() string {
	return "ADDRESS!" + a.AddressID
}

func (a *Address) GetSK() string {
	return "METADATA"
}

const earthRadiusMeters = 6_371_000

func HaversineDistance(lat1, lng1, lat2, lng2 float64) float64 {
	dLat := degreesToRadians(lat2 - lat1)
	dLng := degreesToRadians(lng2 - lng1)

	lat1Rad := degreesToRadians(lat1)
	lat2Rad := degreesToRadians(lat2)

	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1Rad)*math.Cos(lat2Rad)*
			math.Sin(dLng/2)*math.Sin(dLng/2)

	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadiusMeters * c
}

func degreesToRadians(deg float64) float64 {
	return deg * math.Pi / 180
}
