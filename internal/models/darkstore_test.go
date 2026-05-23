package models

import "testing"

// A simple square around central Bengaluru (Indiranagar-ish), ~0.02deg per side.
var squarePolygon = []PolygonPoint{
	{Lat: 12.96, Lng: 77.62},
	{Lat: 12.96, Lng: 77.65},
	{Lat: 12.99, Lng: 77.65},
	{Lat: 12.99, Lng: 77.62},
}

func TestPointInPolygon(t *testing.T) {
	tests := []struct {
		name string
		lat  float64
		lng  float64
		want bool
	}{
		{"centre is inside", 12.975, 77.635, true},
		{"clearly outside (north)", 13.50, 77.635, false},
		{"clearly outside (east)", 12.975, 78.00, false},
		{"just inside near edge", 12.961, 77.621, true},
		{"just outside near edge", 12.959, 77.635, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PointInPolygon(tt.lat, tt.lng, squarePolygon); got != tt.want {
				t.Errorf("PointInPolygon(%v, %v) = %v, want %v", tt.lat, tt.lng, got, tt.want)
			}
		})
	}
}

func TestPointInPolygonDegenerate(t *testing.T) {
	if PointInPolygon(12.975, 77.635, nil) {
		t.Error("nil polygon should never contain a point")
	}
	twoPoints := []PolygonPoint{{Lat: 12.96, Lng: 77.62}, {Lat: 12.99, Lng: 77.65}}
	if PointInPolygon(12.975, 77.635, twoPoints) {
		t.Error("polygon with fewer than 3 points should never contain a point")
	}
}
