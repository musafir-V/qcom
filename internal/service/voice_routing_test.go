package service

import (
	"testing"

	"github.com/qcom/qcom/internal/models"
)

func TestResolveCounterpart(t *testing.T) {
	trip := &models.Trip{DEID: "D1", CustomerUserID: "U9"}

	to, dir, ok := ResolveCounterpart(trip, "de_D1")
	if !ok || to != "cust_U9" || dir != "de_to_cust" {
		t.Fatalf("de caller -> %q %q %v", to, dir, ok)
	}
	to, dir, ok = ResolveCounterpart(trip, "cust_U9")
	if !ok || to != "de_D1" || dir != "cust_to_de" {
		t.Fatalf("cust caller -> %q %q %v", to, dir, ok)
	}
	if _, _, ok := ResolveCounterpart(trip, "de_OTHER"); ok {
		t.Fatal("stranger should not resolve")
	}
	if _, _, ok := ResolveCounterpart(&models.Trip{DEID: "D1"}, "de_D1"); ok {
		t.Fatal("missing customer should not resolve")
	}
}
