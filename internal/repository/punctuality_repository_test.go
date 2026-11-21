package repository

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestPunctualityKeys_SeparateDaysFromClears(t *testing.T) {
	day := punctualityDaySK("2026-06-22", "08:00")
	if day != "PUNCTUALITY_DAY#2026-06-22#08:00" {
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

func TestPunctualityDaySK_IncludesTheStart(t *testing.T) {
	if got := punctualityDaySK("2026-11-21", "08:00"); got != "PUNCTUALITY_DAY#2026-11-21#08:00" {
		t.Fatalf("got %s", got)
	}
}

func TestAppendPunctualityItem_LoadsALegacyDay(t *testing.T) {
	state := PunctualityState{}
	item := map[string]types.AttributeValue{
		"SK":             &types.AttributeValueMemberS{Value: "PUNCTUALITY_DAY#2026-11-21"},
		"date":           &types.AttributeValueMemberS{Value: "2026-11-21"},
		"shift_start":    &types.AttributeValueMemberS{Value: "08:00"},
		"first_scan":     &types.AttributeValueMemberS{Value: "08:13"},
		"signed_minutes": &types.AttributeValueMemberN{Value: "-13"},
		"scored_at":      &types.AttributeValueMemberS{Value: "2026-11-20T06:13:45Z"},
	}
	if err := appendPunctualityItem(&state, item); err != nil {
		t.Fatal(err)
	}
	if len(state.Days) != 1 || state.Days[0].ShiftStart != "08:00" || state.Days[0].SignedMinutes != -13 {
		t.Fatalf("legacy day: %+v", state.Days)
	}
}
