package handlers

import "testing"

func TestParsePolygonLines(t *testing.T) {
	t.Run("empty input returns nil", func(t *testing.T) {
		points, err := parsePolygonLines("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if points != nil {
			t.Fatalf("expected nil points, got %v", points)
		}
	})

	t.Run("whitespace-only input returns nil", func(t *testing.T) {
		points, err := parsePolygonLines("   \n  \n")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if points != nil {
			t.Fatalf("expected nil points, got %v", points)
		}
	})

	t.Run("valid 3-point polygon parses", func(t *testing.T) {
		points, err := parsePolygonLines("12.96,77.62\n12.96,77.66\n12.99,77.66")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(points) != 3 {
			t.Fatalf("expected 3 points, got %d", len(points))
		}
		if points[0].Lat != 12.96 || points[0].Lng != 77.62 {
			t.Fatalf("unexpected first point: %+v", points[0])
		}
	})

	t.Run("fewer than 3 points is an error", func(t *testing.T) {
		if _, err := parsePolygonLines("12.96,77.62\n12.96,77.66"); err == nil {
			t.Fatal("expected error for 2-point polygon")
		}
	})

	t.Run("malformed line is an error", func(t *testing.T) {
		if _, err := parsePolygonLines("12.96,77.62\nnot-a-point\n12.99,77.66"); err == nil {
			t.Fatal("expected error for malformed line")
		}
	})

	t.Run("out-of-range coordinate is an error", func(t *testing.T) {
		if _, err := parsePolygonLines("999,77.62\n12.96,77.66\n12.99,77.66"); err == nil {
			t.Fatal("expected error for out-of-range latitude")
		}
	})
}
