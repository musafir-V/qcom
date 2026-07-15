package models

import "testing"

func TestQRKeys(t *testing.T) {
	c := &QRCampaign{CampaignID: "QC0000000001"}
	if c.GetPK() != "QRCAMPAIGN!QC0000000001" {
		t.Fatalf("campaign PK = %q", c.GetPK())
	}
	if c.GetSK() != "METADATA" {
		t.Fatalf("campaign SK = %q", c.GetSK())
	}
	p := &QRPlacement{Slug: "9xKq2Ab"}
	if p.GetPK() != "QRPLACEMENT!9xKq2Ab" {
		t.Fatalf("placement PK = %q", p.GetPK())
	}
	e := &QRScanEvent{Slug: "9xKq2Ab", CreatedAt: "2026-07-15T10:00:00Z"}
	if e.GetPK() != "QRPLACEMENT!9xKq2Ab" {
		t.Fatalf("event PK = %q", e.GetPK())
	}
	if got := e.GetSK(); got != "SCAN!2026-07-15T10:00:00Z#" {
		t.Fatalf("event SK = %q", got)
	}
}
