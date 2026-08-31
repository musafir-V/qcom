package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

// Reassign builds a DynamoDB transaction with one transact item per DE phone
// key ("DE!{phone}"). If fromDEPhone and toDEPhone are identical, that
// transaction would contain two items with the same key, and DynamoDB raises
// ValidationException rather than a clean, classifiable conflict. The method
// must refuse before ever constructing the transaction — this guards it even
// though the only caller today (AdminService.ReassignTrip) already checks
// this upstream, because the spec anticipates a second caller later.
func TestReassign_SamePhone_RejectedBeforeTransaction(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.PanicLevel)
	// client is intentionally nil: a same-phone request must be rejected
	// before the repository ever touches the DynamoDB client.
	r := &TripRepository{tableName: "test-table", logger: logger}

	err := r.Reassign(context.Background(), "T1", "+260A", "DE-A", "DE-A", "+260A", "ORD-1", "221",
		false, models.TripReassignment{})
	if err == nil {
		t.Fatal("expected an error when fromDEPhone == toDEPhone, got nil")
	}
}

func TestMarkOutForDeliveryUpdateExpression_PreservesFirstDeadline(t *testing.T) {
	expr := markOutForDeliveryUpdateExpression()
	if !strings.Contains(expr, "if_not_exists(drop_deadline, :dd)") {
		t.Fatalf("expression = %q, want if_not_exists so a later write cannot move the first freeze", expr)
	}
	if strings.Contains(expr, "drop_deadline = :dd") {
		t.Fatalf("expression = %q, unconditional SET overwrites a concurrent first freeze", expr)
	}
}

func TestUpdateEditByOrderUpdateExpression_IncludesPackedSnapshotFields(t *testing.T) {
	expr := updateEditByOrderUpdateExpression()
	if !strings.Contains(expr, "#items") {
		t.Fatalf("expression = %q, want #items alias (items is a Dynamo reserved word)", expr)
	}
	if strings.Contains(expr, "SET items ") || strings.Contains(expr, ", items =") {
		t.Fatalf("expression = %q, bare items token is reserved", expr)
	}
	for _, field := range []string{"payment", "tasks", "updated_at"} {
		if !strings.Contains(expr, field) {
			t.Fatalf("expression = %q, want SET to include %q", expr, field)
		}
	}
}

func TestUpdateEditByOrderConditionExpression_MatchesUpdatePayment(t *testing.T) {
	cond := updateEditByOrderConditionExpression()
	want := "attribute_exists(PK) AND #status <> :completed AND #status <> :cancelled AND #status <> :distance_failed"
	if cond != want {
		t.Fatalf("condition = %q, want %q", cond, want)
	}
}
