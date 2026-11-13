package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
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
	want := "attribute_exists(PK) AND #status <> :completed AND #status <> :cancelled AND #status <> :distance_failed AND #status <> :inactive"
	if cond != want {
		t.Fatalf("condition = %q, want %q", cond, want)
	}
}

func TestAssignmentBlocksCreate_ExistingDistanceFailedDoesNotRetry(t *testing.T) {
	blocked, backfill := assignmentBlocksCreate(distanceFailedGuardAbsent, false, true)
	if !blocked || !backfill {
		t.Fatalf("existing distance_failed must block create and backfill the guard, blocked=%v backfill=%v", blocked, backfill)
	}
}

func TestAssignmentBlocksCreate_SuppressedGuardBlocksWithoutBackfill(t *testing.T) {
	blocked, backfill := assignmentBlocksCreate(distanceFailedGuardSuppressed, false, false)
	if !blocked || backfill {
		t.Fatalf("suppressed guard blocks without another backfill, blocked=%v backfill=%v", blocked, backfill)
	}
}

func TestAssignmentBlocksCreate_ReleasedGuardAllowsOneRetry(t *testing.T) {
	blocked, backfill := assignmentBlocksCreate(distanceFailedGuardReleased, false, true)
	if blocked || backfill {
		t.Fatalf("released guard must allow one retry despite old distance_failed rows, blocked=%v backfill=%v", blocked, backfill)
	}
}

func TestAssignmentBlocksCreate_LiveTripWins(t *testing.T) {
	blocked, backfill := assignmentBlocksCreate(distanceFailedGuardAbsent, true, true)
	if blocked || backfill {
		t.Fatalf("live trip must stay assignable, blocked=%v backfill=%v", blocked, backfill)
	}
}

func TestNoteOrderIndexStatus_DistanceFailedIsNotHydrated(t *testing.T) {
	seen := map[string]bool{}
	liveID, failed := noteOrderIndexStatus(string(models.TripStatusDistanceFailed), "T1", seen)
	if liveID != "" || !failed {
		t.Fatalf("distance_failed liveID=%q failed=%v", liveID, failed)
	}
	liveID, failed = noteOrderIndexStatus(string(models.TripStatusCreated), "T2", seen)
	if liveID != "T2" || failed {
		t.Fatalf("created liveID=%q failed=%v", liveID, failed)
	}
	liveID, failed = noteOrderIndexStatus(string(models.TripStatusCancelled), "T3", seen)
	if liveID != "" || failed {
		t.Fatalf("cancelled liveID=%q failed=%v", liveID, failed)
	}
	liveID, failed = noteOrderIndexStatus("", "T4", seen)
	if liveID != "T4" || failed {
		t.Fatalf("missing status should hydrate, liveID=%q failed=%v", liveID, failed)
	}
	liveID, _ = noteOrderIndexStatus(string(models.TripStatusCreated), "T2", seen)
	if liveID != "" {
		t.Fatalf("duplicate trip id should not hydrate twice, liveID=%q", liveID)
	}
}

func TestDistanceFailedGuardSuppress_DoesNotTouchOrderIndex(t *testing.T) {
	w := distanceFailedGuardSuppress("tbl", "ORD0136933544", "2026-11-13T00:00:00Z")
	if w.Update == nil {
		t.Fatal("expected Update")
	}
	pk, ok := w.Update.Key["PK"].(*types.AttributeValueMemberS)
	if !ok || pk.Value != "DISTFAIL!ORD0136933544" {
		t.Fatalf("PK = %#v", w.Update.Key["PK"])
	}
	expr := aws.ToString(w.Update.UpdateExpression)
	if strings.Contains(expr, "trip_order_id") {
		t.Fatalf("guard update must not set trip_order_id: %s", expr)
	}
}
