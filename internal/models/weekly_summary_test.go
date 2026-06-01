package models

import "testing"

func TestDEWeeklySummaryKeys(t *testing.T) {
	w := &DEWeeklySummary{
		DEID:          "+260971234567",
		WeekStartDate: "2026-05-25",
	}
	if got, want := w.GetPK(), "WEEKLY!+260971234567"; got != want {
		t.Errorf("GetPK() = %q, want %q", got, want)
	}
	if got, want := w.GetSK(), "2026-05-25"; got != want {
		t.Errorf("GetSK() = %q, want %q", got, want)
	}
}
