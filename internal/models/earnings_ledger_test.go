package models

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
)

func TestEarningsLedgerKeys(t *testing.T) {
	e := &EarningsLedger{
		DEID:      "+260971234567",
		EarningID: "earn-123",
		CreatedAt: "2026-06-02T04:00:00Z",
	}
	if got, want := e.GetPK(), "EARN!+260971234567"; got != want {
		t.Errorf("GetPK() = %q, want %q", got, want)
	}
	if got, want := e.GetSK(), "2026-06-02T04:00:00Z#earn-123"; got != want {
		t.Errorf("GetSK() = %q, want %q", got, want)
	}
}

func TestEarningsLedgerDistanceKMRoundTrips(t *testing.T) {
	e := &EarningsLedger{
		DEID:       "+260971234567",
		EarningID:  "earn-123",
		Type:       EarningTypeTrip,
		AmountZMW:  50,
		CreatedAt:  "2026-06-02T04:00:00Z",
		DistanceKM: 5.2,
	}
	item, err := attributevalue.MarshalMap(e)
	if err != nil {
		t.Fatalf("MarshalMap failed: %v", err)
	}
	var got EarningsLedger
	if err := attributevalue.UnmarshalMap(item, &got); err != nil {
		t.Fatalf("UnmarshalMap failed: %v", err)
	}
	if got.DistanceKM != 5.2 {
		t.Fatalf("DistanceKM round-trip = %v, want 5.2", got.DistanceKM)
	}
}
