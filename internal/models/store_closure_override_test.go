package models

import (
	"testing"
	"time"

	"github.com/qcom/qcom/internal/timezone"
)

func TestIsElectionDayClosure(t *testing.T) {
	loc := timezone.ZambiaLocation()
	cases := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"election day morning", time.Date(2026, 8, 13, 6, 0, 0, 0, loc), true},
		{"election day midnight", time.Date(2026, 8, 13, 0, 0, 0, 0, loc), true},
		{"election day end", time.Date(2026, 8, 13, 23, 59, 0, 0, loc), true},
		{"day before", time.Date(2026, 8, 12, 12, 0, 0, 0, loc), false},
		{"day after", time.Date(2026, 8, 14, 12, 0, 0, 0, loc), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsElectionDayClosure(tc.t); got != tc.want {
				t.Fatalf("IsElectionDayClosure() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNextOpensAtAfterClosureDay(t *testing.T) {
	ds := Darkstore{OpensAt: "08:00", ClosesAt: "22:00"}
	next, ok := ds.NextOpensAtAfterClosureDay(ElectionDayClosureDate)
	if !ok {
		t.Fatal("expected next opens_at")
	}
	if next != "2026-08-14T08:00:00+02:00" {
		t.Fatalf("expected opens day after election, got %q", next)
	}
}
