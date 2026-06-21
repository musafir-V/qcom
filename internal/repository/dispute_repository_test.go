// internal/repository/dispute_repository_test.go
package repository

import "testing"

func TestBuildOpenGuardKey(t *testing.T) {
	pk, sk := buildOpenGuardKey("order-9")
	if pk != "DISPUTEOPEN!order-9" {
		t.Errorf("guard PK = %q, want DISPUTEOPEN!order-9", pk)
	}
	if sk != "METADATA" {
		t.Errorf("guard SK = %q, want METADATA", sk)
	}
}
