package service

import (
	"io"
	"testing"

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
}
