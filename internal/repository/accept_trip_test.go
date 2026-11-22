package repository

import (
	"strings"
	"testing"
)

func TestAcceptTripUpdateExpression_RecordsAcceptedAt(t *testing.T) {
	expr := acceptTripUpdateExpression()
	if !strings.Contains(expr, "accepted_at = :now") {
		t.Fatalf("accept update %q does not record accepted_at", expr)
	}
	if !strings.Contains(expr, "#status = :accepted") {
		t.Fatalf("accept update %q does not accept the trip", expr)
	}
}
