// internal/repository/dispute_repository_test.go
package repository

import (
	"testing"

	"github.com/qcom/qcom/internal/models"
)

func TestBuildOpenGuardKey(t *testing.T) {
	pk, sk := buildOpenGuardKey("order-9")
	if pk != "DISPUTEOPEN!order-9" {
		t.Errorf("guard PK = %q, want DISPUTEOPEN!order-9", pk)
	}
	if sk != "METADATA" {
		t.Errorf("guard SK = %q, want METADATA", sk)
	}
}

func TestDisputeStatusIndexFor(t *testing.T) {
	t.Run("no store falls back to the status index", func(t *testing.T) {
		idx, attr, val := disputeStatusIndexFor("")
		if idx != "DisputeStatusIndex" {
			t.Errorf("index = %q, want DisputeStatusIndex", idx)
		}
		if attr != "dispute_status_key" {
			t.Errorf("attr = %q, want dispute_status_key", attr)
		}
		if val != "" {
			t.Errorf("value prefix = %q, want empty", val)
		}
	})

	t.Run("store selects the composite index", func(t *testing.T) {
		idx, attr, val := disputeStatusIndexFor("42")
		if idx != "DisputeStoreStatusIndex" {
			t.Errorf("index = %q, want DisputeStoreStatusIndex", idx)
		}
		if attr != "dispute_store_status_key" {
			t.Errorf("attr = %q, want dispute_store_status_key", attr)
		}
		if val != "42#" {
			t.Errorf("value prefix = %q, want %q", val, "42#")
		}
	})

	t.Run("unknown store is an ordinary store value", func(t *testing.T) {
		idx, _, val := disputeStatusIndexFor(models.UnknownStoreID)
		if idx != "DisputeStoreStatusIndex" {
			t.Errorf("index = %q, want DisputeStoreStatusIndex", idx)
		}
		if val != "UNKNOWN#" {
			t.Errorf("value prefix = %q, want %q", val, "UNKNOWN#")
		}
	})
}

// TestDisputeStatusIndexFor_MatchesWritePathKey guards against the write path
// (models.DisputeStoreStatusKeyFor) and the read path (disputeStatusIndexFor's
// prefix + status) drifting apart. They build the same "<store>#<status>" key
// format independently, in different packages, and each is currently pinned
// only by hardcoded literals in its own package's tests. Nothing else couples
// them, and a divergence would be silent: queries would just return nothing.
func TestDisputeStatusIndexFor_MatchesWritePathKey(t *testing.T) {
	cases := []struct {
		storeID string
		status  models.DisputeStatus
	}{
		{"42", models.DisputeStatusOpen},
		{"042", models.DisputeStatusOpen},
		{"221", models.DisputeStatusResolved},
		{models.UnknownStoreID, models.DisputeStatusUnderReview},
	}
	for _, tc := range cases {
		_, _, prefix := disputeStatusIndexFor(tc.storeID)
		read := prefix + string(tc.status)
		write := models.DisputeStoreStatusKeyFor(tc.storeID, tc.status)
		if read != write {
			t.Errorf("read-path key %q != write-path key %q (store %q, status %q)",
				read, write, tc.storeID, tc.status)
		}
	}
}
