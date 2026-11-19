package repository

import (
	"testing"
	"time"
)

func TestPunctualityKeys_SeparateDaysFromClears(t *testing.T) {
	day := punctualityDaySK("2026-06-22")
	if day != "PUNCTUALITY_DAY#2026-06-22" {
		t.Fatalf("day sk = %s", day)
	}
	at := time.Date(2026, 6, 22, 16, 0, 0, 0, time.UTC)
	clear := punctualityClearSK(at)
	if clear != "PUNCTUALITY_CLEAR#2026-06-22T16:00:00Z" {
		t.Fatalf("clear sk = %s", clear)
	}
}

func TestDecodeShiftStarts_ReadsTheListAndFallsBackToOneTime(t *testing.T) {
	raw := `[{"time":"16:00","set_at":"2026-06-22T04:00:00Z"},{"time":"08:00","set_at":"2026-06-22T04:00:00Z"}]`
	got, err := decodeShiftStarts(raw, "09:00", "2026-06-22T05:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Time != "16:00" || got[1].Time != "08:00" {
		t.Fatalf("list = %+v", got)
	}

	fallback, err := decodeShiftStarts("", "08:00", "2026-06-22T04:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(fallback) != 1 || fallback[0].Time != "08:00" {
		t.Fatalf("fallback = %+v", fallback)
	}
}
