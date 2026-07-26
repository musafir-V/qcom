// internal/models/dispute_test.go
package models

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
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

func TestCanTransitionDispute(t *testing.T) {
	cases := []struct {
		from, to DisputeStatus
		want     bool
	}{
		{DisputeStatusOpen, DisputeStatusUnderReview, true},
		{DisputeStatusOpen, DisputeStatusResolved, true},
		{DisputeStatusOpen, DisputeStatusRejected, true},
		{DisputeStatusUnderReview, DisputeStatusResolved, true},
		{DisputeStatusUnderReview, DisputeStatusRejected, true},
		{DisputeStatusUnderReview, DisputeStatusOpen, false},
		{DisputeStatusResolved, DisputeStatusOpen, false},
		{DisputeStatusRejected, DisputeStatusUnderReview, false},
		{DisputeStatusOpen, DisputeStatusOpen, false},
		{DisputeStatus("BOGUS"), DisputeStatusResolved, false},
	}
	for _, c := range cases {
		if got := CanTransitionDispute(c.from, c.to); got != c.want {
			t.Errorf("CanTransitionDispute(%q,%q)=%v want %v", c.from, c.to, got, c.want)
		}
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

func TestDisputeStoreStatusKeyFor(t *testing.T) {
	cases := []struct {
		name    string
		storeID string
		status  DisputeStatus
		want    string
	}{
		{"store and status", "42", DisputeStatusOpen, "42#OPEN"},
		{"unknown store", UnknownStoreID, DisputeStatusOpen, "UNKNOWN#OPEN"},
		{"resolved", "7", DisputeStatusResolved, "7#RESOLVED"},
		{"empty store yields empty key", "", DisputeStatusOpen, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DisputeStoreStatusKeyFor(tc.storeID, tc.status); got != tc.want {
				t.Errorf("DisputeStoreStatusKeyFor(%q, %q) = %q, want %q", tc.storeID, tc.status, got, tc.want)
			}
		})
	}
}

func TestUnknownStoreIDValue(t *testing.T) {
	if UnknownStoreID != "UNKNOWN" {
		t.Errorf("UnknownStoreID = %q, want %q", UnknownStoreID, "UNKNOWN")
	}
}

func TestDisputeMarshalOmitsEmptyStoreAttributes(t *testing.T) {
	d := Dispute{DisputeID: "DP1", OrderNumber: "ORD1", Status: DisputeStatusOpen}
	item, err := attributevalue.MarshalMap(d)
	if err != nil {
		t.Fatalf("MarshalMap: %v", err)
	}
	if _, ok := item["store_id"]; ok {
		t.Error("store_id attribute present on a dispute with no store; must be omitted")
	}
	if _, ok := item["dispute_store_status_key"]; ok {
		t.Error("dispute_store_status_key present with no store; must be omitted to keep the GSI sparse")
	}
}

func TestDisputeMarshalIncludesStoreAttributesWhenSet(t *testing.T) {
	d := Dispute{
		DisputeID:             "DP1",
		OrderNumber:           "ORD1",
		Status:                DisputeStatusOpen,
		StoreID:               "42",
		DisputeStoreStatusKey: DisputeStoreStatusKeyFor("42", DisputeStatusOpen),
	}
	item, err := attributevalue.MarshalMap(d)
	if err != nil {
		t.Fatalf("MarshalMap: %v", err)
	}
	got, ok := item["dispute_store_status_key"].(*types.AttributeValueMemberS)
	if !ok {
		t.Fatalf("dispute_store_status_key missing or wrong type: %#v", item["dispute_store_status_key"])
	}
	if got.Value != "42#OPEN" {
		t.Errorf("dispute_store_status_key = %q, want %q", got.Value, "42#OPEN")
	}
}
