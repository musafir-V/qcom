// internal/models/dispute_test.go
package models

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
)

func TestDisputeKeys(t *testing.T) {
	d := &Dispute{DisputeID: "abc"}
	if got := d.GetPK(); got != "DISPUTE!abc" {
		t.Errorf("GetPK() = %q, want DISPUTE!abc", got)
	}
	if got := d.GetSK(); got != "METADATA" {
		t.Errorf("GetSK() = %q, want METADATA", got)
	}
}

func TestDisputeOpenGuardPK(t *testing.T) {
	if got := DisputeOpenGuardPK("order-7"); got != "DISPUTEOPEN!order-7" {
		t.Errorf("DisputeOpenGuardPK() = %q, want DISPUTEOPEN!order-7", got)
	}
}

func TestDisputeDispositionSK(t *testing.T) {
	if got := DisputeDispositionSK("ITEM_MISSING"); got != "DISPUTE_DISPOSITION!ITEM_MISSING" {
		t.Errorf("DisputeDispositionSK() = %q, want DISPUTE_DISPOSITION!ITEM_MISSING", got)
	}
}

func TestDisputeMarshalHasNoOrderIDAttribute(t *testing.T) {
	d := &Dispute{
		DisputeID:          "d1",
		OrderNumber:        "ORD123",
		DisputeOrderNumber: "ORD123",
		CustomerID:         "c1",
		Status:             DisputeStatusOpen,
		CreatedAt:          "2026-06-21T00:00:00Z",
		UpdatedAt:          "2026-06-21T00:00:00Z",
	}
	m, err := attributevalue.MarshalMap(d)
	if err != nil {
		t.Fatalf("MarshalMap failed: %v", err)
	}
	if _, ok := m["order_id"]; ok {
		t.Errorf("marshaled map must NOT have key \"order_id\" (would pollute OrderIndex GSI), got: %v", m)
	}
	if _, ok := m["order_number"]; !ok {
		t.Errorf("marshaled map must have key \"order_number\", got keys: %v", m)
	}
	if _, ok := m["dispute_order_number"]; !ok {
		t.Errorf("marshaled map must have key \"dispute_order_number\", got keys: %v", m)
	}
}
