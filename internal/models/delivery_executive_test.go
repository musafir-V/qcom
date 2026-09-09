package models

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestTripsToday(t *testing.T) {
	de := &DeliveryExecutive{DailyTripCount: 5, DailyCountDate: "2026-06-09"}

	if got := de.TripsToday("2026-06-09"); got != 5 {
		t.Fatalf("same day: expected 5, got %d", got)
	}
	if got := de.TripsToday("2026-06-10"); got != 0 {
		t.Fatalf("stale day: expected 0, got %d", got)
	}

	empty := &DeliveryExecutive{}
	if got := empty.TripsToday("2026-06-09"); got != 0 {
		t.Fatalf("never worked: expected 0, got %d", got)
	}
}

func TestCashExceeds(t *testing.T) {
	cases := []struct {
		name   string
		inHand float64
		limit  float64
		want   bool
	}{
		{"under limit", 400, 500, false},
		{"exactly at limit is allowed (strict >)", 500, 500, false},
		{"over limit by a cent", 500.01, 500, true},
		{"well over", 800, 500, true},
		{"zero cash", 0, 500, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			de := &DeliveryExecutive{InHandCashZMW: c.inHand}
			if got := de.CashExceeds(c.limit); got != c.want {
				t.Fatalf("CashExceeds(%v) with in-hand %v = %v, want %v", c.limit, c.inHand, got, c.want)
			}
		})
	}
}

func TestDeliveryExecutive_MissingArchivedUnmarshalsFalse(t *testing.T) {
	item := map[string]types.AttributeValue{
		"phone_number": &types.AttributeValueMemberS{Value: "+260971000001"},
		"name":         &types.AttributeValueMemberS{Value: "Ada"},
		"status":       &types.AttributeValueMemberS{Value: "offline"},
	}

	var de DeliveryExecutive
	if err := attributevalue.UnmarshalMap(item, &de); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if de.Archived {
		t.Fatal("old items missing archived must unmarshal as false")
	}
	if de.ArchivedAt != "" {
		t.Fatalf("archived_at = %q, want empty", de.ArchivedAt)
	}
}
