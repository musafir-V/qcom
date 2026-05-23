package models

// PolygonPoint is a single vertex of a darkstore's serviceable-area polygon.
type PolygonPoint struct {
	Lat float64 `json:"lat" dynamodbav:"lat"`
	Lng float64 `json:"lng" dynamodbav:"lng"`
}

// Darkstore is a fulfilment centre with a serviceable-area polygon.
// Polygons are assumed non-overlapping, so any point falls inside at most one.
type Darkstore struct {
	DarkstoreID string         `json:"darkstore_id" dynamodbav:"darkstore_id"`
	Name        string         `json:"name" dynamodbav:"name"`
	Latitude    float64        `json:"latitude" dynamodbav:"latitude"`
	Longitude   float64        `json:"longitude" dynamodbav:"longitude"`
	Polygon     []PolygonPoint `json:"polygon" dynamodbav:"polygon"`
	IsActive    bool           `json:"is_active" dynamodbav:"is_active"`
	CreatedAt   string         `json:"created_at" dynamodbav:"created_at"`
	UpdatedAt   string         `json:"updated_at" dynamodbav:"updated_at"`
}

func (d *Darkstore) GetPK() string {
	return "DARKSTORE!" + d.DarkstoreID
}

func (d *Darkstore) GetSK() string {
	return "METADATA"
}

// Contains reports whether the given coordinate lies inside the darkstore's polygon.
func (d *Darkstore) Contains(lat, lng float64) bool {
	return PointInPolygon(lat, lng, d.Polygon)
}

// PointInPolygon runs the ray-casting (even-odd) test. Coordinates are treated
// as planar (lng = x, lat = y), which is accurate at city-scale darkstore zones.
// The polygon ring may be open or closed; the closing edge is handled implicitly.
func PointInPolygon(lat, lng float64, polygon []PolygonPoint) bool {
	n := len(polygon)
	if n < 3 {
		return false
	}

	inside := false
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		xi, yi := polygon[i].Lng, polygon[i].Lat
		xj, yj := polygon[j].Lng, polygon[j].Lat

		if (yi > lat) != (yj > lat) &&
			lng < (xj-xi)*(lat-yi)/(yj-yi)+xi {
			inside = !inside
		}
	}

	return inside
}
