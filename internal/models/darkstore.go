package models

// defaultPresenceRadiusMeters is the tight per-store presence geofence used
// when a darkstore has no explicit presence_radius_meters configured.
const defaultPresenceRadiusMeters = 75.0

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
	// PresenceRadiusMeters is the tight pod geofence around Latitude/Longitude
	// used for rider presence scans (default 75 when zero). This is NOT the
	// serviceability Polygon (kilometres wide) — that stays for customers only.
	PresenceRadiusMeters float64 `json:"presence_radius_meters,omitempty" dynamodbav:"presence_radius_meters,omitempty"`
	IsActive             bool    `json:"is_active" dynamodbav:"is_active"`
	OpensAt     string         `json:"opens_at" dynamodbav:"opens_at"`
	ClosesAt    string         `json:"closes_at" dynamodbav:"closes_at"`
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

// EffectivePresenceRadiusMeters returns the configured presence radius, or the
// default (75 m) when unset/zero.
func (d *Darkstore) EffectivePresenceRadiusMeters() float64 {
	if d.PresenceRadiusMeters <= 0 {
		return defaultPresenceRadiusMeters
	}
	return d.PresenceRadiusMeters
}

// DistanceMeters returns the great-circle (haversine) distance in metres from
// the darkstore centre (Latitude/Longitude) to (lat, lng). Unlike PointInPolygon
// this is spherical, which matters at the tens-of-metres presence scale.
func (d *Darkstore) DistanceMeters(lat, lng float64) float64 {
	return HaversineDistance(d.Latitude, d.Longitude, lat, lng)
}

// WithinPresence reports whether a location fix is inside the pod geofence,
// giving the rider the benefit of their GPS error circle (accuracyM). A fix is
// accepted when distance-to-centre minus the accuracy radius is within the
// presence radius.
func (d *Darkstore) WithinPresence(lat, lng, accuracyM float64) bool {
	return d.DistanceMeters(lat, lng)-accuracyM <= d.EffectivePresenceRadiusMeters()
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
