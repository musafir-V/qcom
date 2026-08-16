package service

import (
	"context"
	"io"
	"testing"

	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

// newBypassTestService builds a ServiceabilityService with only the bypass
// allowlist wired. All external dependencies are nil because the bypass
// decision + base-result construction never touch them.
func newBypassTestService(bypassUserIDs []string) *ServiceabilityService {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return NewServiceabilityService(nil, nil, nil, nil, logger, false, bypassUserIDs)
}

func TestIsBypassUser(t *testing.T) {
	svc := newBypassTestService([]string{"user-abc", "user-def"})

	cases := []struct {
		name   string
		userID string
		want   bool
	}{
		{"allowlisted", "user-abc", true},
		{"allowlisted second", "user-def", true},
		{"not allowlisted", "user-xyz", false},
		{"empty user id (guest)", "", false},
		{"case sensitive miss", "USER-ABC", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := svc.isBypassUser(tc.userID); got != tc.want {
				t.Fatalf("isBypassUser(%q) = %v, want %v", tc.userID, got, tc.want)
			}
		})
	}
}

func TestIsBypassUser_EmptyAllowlistIsNoOp(t *testing.T) {
	svc := newBypassTestService(nil)
	if svc.isBypassUser("user-abc") {
		t.Fatal("expected empty allowlist to bypass no one")
	}
}

// A stray comma / blank entry in the env value must not accidentally match an
// empty user id.
func TestIsBypassUser_BlankEntriesDropped(t *testing.T) {
	svc := newBypassTestService([]string{"", "user-abc", ""})
	if svc.isBypassUser("") {
		t.Fatal("empty user id must never match, even with blank allowlist entries")
	}
	if !svc.isBypassUser("user-abc") {
		t.Fatal("expected user-abc to match")
	}
}

func TestNewBypassResult(t *testing.T) {
	result := newBypassResult()

	if !result.Serviceable {
		t.Error("bypass result must be serviceable")
	}
	if result.DarkstoreID != bypassDarkstoreID {
		t.Errorf("darkstore_id = %q, want %q", result.DarkstoreID, bypassDarkstoreID)
	}
	if result.DarkstoreID != "100" {
		t.Errorf("expected fixed dummy store 100, got %q", result.DarkstoreID)
	}
	if result.IsOperational == nil || !*result.IsOperational {
		t.Error("bypass result must be operational (is_operational=true)")
	}
	if result.OperatingHours != nil {
		t.Error("bypass result must not include operating_hours")
	}
	if result.ETAMinutes == nil || *result.ETAMinutes != bypassETAMinutes {
		t.Errorf("eta_minutes = %v, want %d", result.ETAMinutes, bypassETAMinutes)
	}
	if *result.ETAMinutes != 7 {
		t.Errorf("expected hardcoded 7-minute ETA, got %d", *result.ETAMinutes)
	}
	// The base builder leaves the address to the caller.
	if result.ResolvedAddress != nil {
		t.Error("base bypass result must not set resolved_address")
	}
	// Dummy store 100 has no real coordinates.
	if result.Latitude != 0 || result.Longitude != 0 {
		t.Errorf("bypass result must omit store coordinates, got lat=%v lng=%v", result.Latitude, result.Longitude)
	}
}

func TestAttachStore(t *testing.T) {
	ds := &models.Darkstore{
		DarkstoreID: "221",
		Latitude:    -15.4167,
		Longitude:   28.2833,
	}
	result := attachStore(&ServiceabilityResult{Serviceable: true}, ds)
	if result.DarkstoreID != "221" {
		t.Errorf("darkstore_id = %q, want 221", result.DarkstoreID)
	}
	if result.Latitude != -15.4167 {
		t.Errorf("latitude = %v, want -15.4167", result.Latitude)
	}
	if result.Longitude != 28.2833 {
		t.Errorf("longitude = %v, want 28.2833", result.Longitude)
	}
	if !result.Serviceable {
		t.Error("attachStore must leave other fields intact")
	}
}

func TestExcludeDarkstoreID(t *testing.T) {
	stores := []models.Darkstore{
		{DarkstoreID: "100"},
		{DarkstoreID: "221"},
		{DarkstoreID: "100"},
	}
	filtered := excludeDarkstoreID(stores, bypassDarkstoreID)
	if len(filtered) != 1 || filtered[0].DarkstoreID != "221" {
		t.Fatalf("excludeDarkstoreID() = %+v, want only store 221", filtered)
	}
}

func TestMatchDarkstore_ISTestUsesFirstNonBypassStore(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	svc := NewServiceabilityService(nil, nil, nil, nil, logger, true, nil)

	stores := []models.Darkstore{
		{DarkstoreID: bypassDarkstoreID, IsActive: true},
		{DarkstoreID: "221", IsActive: true},
	}
	filtered := excludeDarkstoreID(stores, bypassDarkstoreID)

	op := logging.Start(context.Background(), logger, "TestMatchDarkstore", nil)
	defer op.End()

	matched := svc.matchDarkstore(op, filtered, 12.97, 77.71)
	if matched == nil || matched.DarkstoreID != "221" {
		t.Fatalf("matchDarkstore() = %v, want store 221", matched)
	}
}
