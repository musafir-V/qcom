package repository

import (
	"context"
	"errors"
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

func TestMarkAdminOFDInboundUpdateExpression_SetsFlagOnTripMetadata(t *testing.T) {
	expr := markAdminOFDInboundUpdateExpression()
	if !strings.Contains(expr, "admin_ofd_inbound = :t") {
		t.Fatalf("expression = %q, want SET admin_ofd_inbound = :t on TRIP METADATA", expr)
	}
	if !strings.Contains(expr, "updated_at = :now") {
		t.Fatalf("expression = %q, want updated_at", expr)
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

func txCanceled(codes ...string) *types.TransactionCanceledException {
	reasons := make([]types.CancellationReason, 0, len(codes))
	for _, c := range codes {
		reasons = append(reasons, types.CancellationReason{Code: aws.String(c)})
	}
	return &types.TransactionCanceledException{
		Message:             aws.String("canceled"),
		CancellationReasons: reasons,
	}
}

func TestClassifyCompleteTripOnlyErr(t *testing.T) {
	if err := classifyCompleteTripOnlyErr(nil); err != nil {
		t.Fatalf("nil err: %v", err)
	}
	wrapped := classifyCompleteTripOnlyErr(errors.New("network"))
	if wrapped == nil || !strings.Contains(wrapped.Error(), "failed to complete trip") {
		t.Fatalf("other err: got %v", wrapped)
	}

	cond := classifyCompleteTripOnlyErr(txCanceled("ConditionalCheckFailed"))
	if !errors.Is(cond, ErrTripTerminal) {
		t.Fatalf("ConditionalCheckFailed: got %v, want ErrTripTerminal", cond)
	}

	conflict := classifyCompleteTripOnlyErr(txCanceled("TransactionConflict"))
	if errors.Is(conflict, ErrTripTerminal) {
		t.Fatal("TransactionConflict must not be ErrTripTerminal")
	}
	if conflict == nil || !strings.Contains(conflict.Error(), "failed to complete trip") {
		t.Fatalf("TransactionConflict: got %v, want failed-to-complete", conflict)
	}

	throughput := classifyCompleteTripOnlyErr(txCanceled("ProvisionedThroughputExceeded"))
	if errors.Is(throughput, ErrTripTerminal) {
		t.Fatal("ProvisionedThroughputExceeded must not be ErrTripTerminal")
	}
	if throughput == nil || !strings.Contains(throughput.Error(), "failed to complete trip") {
		t.Fatalf("ProvisionedThroughputExceeded: got %v, want failed-to-complete", throughput)
	}

	empty := classifyCompleteTripOnlyErr(txCanceled())
	if errors.Is(empty, ErrTripTerminal) {
		t.Fatal("empty CancellationReasons must not be ErrTripTerminal")
	}
	if empty == nil || !strings.Contains(empty.Error(), "failed to complete trip") {
		t.Fatalf("empty reasons: got %v, want failed-to-complete", empty)
	}
}

func TestCompleteTripOnly_WithoutClientReturnsError(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.PanicLevel)
	r := &TripRepository{tableName: "test-table", logger: logger}
	err := r.CompleteTripOnly(context.Background(), "T1", []models.Task{
		{TaskID: "p", Type: models.TaskTypePickup},
	})
	if err == nil {
		t.Fatal("expected error when Dynamo client is unset")
	}
}

func TestCompleteTripOnly_NoDEInTransact(t *testing.T) {
	items, err := completeTripOnlyTransactItems("tbl", "T-99", []models.Task{
		{TaskID: "p", Type: models.TaskTypePickup},
	}, "2026-09-01T00:00:00Z")
	if err != nil {
		t.Fatalf("completeTripOnlyTransactItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 transact item (trip only, no DE), got %d", len(items))
	}
	for i, it := range items {
		if it.Update == nil {
			t.Fatalf("item %d: expected Update", i)
		}
		pkAV, ok := it.Update.Key["PK"].(*types.AttributeValueMemberS)
		if !ok {
			t.Fatalf("item %d: PK missing", i)
		}
		if strings.HasPrefix(pkAV.Value, "DE!") {
			t.Fatalf("must not include DE! PK in transact, got %q", pkAV.Value)
		}
		if pkAV.Value != "TRIP!T-99" {
			t.Fatalf("PK = %q, want TRIP!T-99", pkAV.Value)
		}
		expr := aws.ToString(it.Update.UpdateExpression)
		for _, want := range []string{"SET tasks", "#status", "completed_at", "updated_at"} {
			if !strings.Contains(expr, want) {
				t.Fatalf("update expr %q missing %q", expr, want)
			}
		}
		cond := aws.ToString(it.Update.ConditionExpression)
		if !strings.Contains(cond, "#status <> :completed") || !strings.Contains(cond, "#status <> :cancelled") {
			t.Fatalf("condition = %q, want not completed/cancelled", cond)
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
