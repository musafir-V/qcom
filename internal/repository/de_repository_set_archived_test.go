package repository

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/sirupsen/logrus"
)

func testDERepo(t *testing.T, handler http.HandlerFunc) *DERepository {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := dynamodb.New(dynamodb.Options{
		Region:                          "us-east-1",
		Credentials:                     credentials.NewStaticCredentialsProvider("AKID", "SECRET", ""),
		BaseEndpoint:                    aws.String(srv.URL),
		HTTPClient:                      srv.Client(),
		RetryMaxAttempts:                1,
		DisableValidateResponseChecksum: true,
	})
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return NewDERepository(client, "test-table", logger)
}

func TestSetArchived_NotFound(t *testing.T) {
	repo := testDERepo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.Header().Set("X-Amzn-Errortype", "ConditionalCheckFailedException")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"The conditional request failed"}`))
	})

	got, err := repo.SetArchived(context.Background(), "+260971000001", true)
	if !errors.Is(err, ErrDENotFound) {
		t.Fatalf("err = %v, want ErrDENotFound", err)
	}
	if got != nil {
		t.Fatalf("de = %#v, want nil", got)
	}
}

func TestSetArchived_ArchiveReturnsUpdated(t *testing.T) {
	repo := testDERepo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		_, _ = w.Write([]byte(`{"Attributes":{"phone_number":{"S":"+260971000001"},"archived":{"BOOL":true},"archived_at":{"S":"2026-09-09T12:00:00Z"},"status":{"S":"offline"}}}`))
	})

	got, err := repo.SetArchived(context.Background(), "+260971000001", true)
	if err != nil {
		t.Fatalf("SetArchived: %v", err)
	}
	if got == nil || !got.Archived || got.ArchivedAt == "" {
		t.Fatalf("got %#v", got)
	}
	if got.PhoneNumber != "+260971000001" {
		t.Fatalf("phone = %q", got.PhoneNumber)
	}
}

func TestSetArchived_RestoreClearsArchivedAt(t *testing.T) {
	repo := testDERepo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		_, _ = w.Write([]byte(`{"Attributes":{"phone_number":{"S":"+260971000001"},"archived":{"BOOL":false},"status":{"S":"offline"}}}`))
	})

	got, err := repo.SetArchived(context.Background(), "+260971000001", false)
	if err != nil {
		t.Fatalf("SetArchived: %v", err)
	}
	if got == nil || got.Archived || got.ArchivedAt != "" {
		t.Fatalf("got %#v", got)
	}
}

func TestSetArchived_UpdateError(t *testing.T) {
	repo := testDERepo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.Header().Set("X-Amzn-Errortype", "InternalServerError")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"__type":"com.amazonaws.dynamodb.v20120810#InternalServerError","message":"boom"}`))
	})

	_, err := repo.SetArchived(context.Background(), "+260971000001", true)
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrDENotFound) {
		t.Fatal("generic update error must not be ErrDENotFound")
	}
}

func TestBuildArchiveActiveUpdate_AtomicOfflineAndArchived(t *testing.T) {
	expr, names, values, cond := buildArchiveActiveUpdate("2026-09-09T12:00:00Z")
	if !strings.Contains(expr, "#status = :offline") || !strings.Contains(expr, "archived = :t") || !strings.Contains(expr, "archived_at = :now") {
		t.Fatalf("expr missing atomic status+archived set: %q", expr)
	}
	if !strings.Contains(expr, "REMOVE") || !strings.Contains(expr, "duty_index_key") {
		t.Fatalf("expr must clear duty fields: %q", expr)
	}
	if names["#status"] != "status" {
		t.Fatalf("names = %v", names)
	}
	if s, ok := values[":offline"].(*types.AttributeValueMemberS); !ok || s.Value != "offline" {
		t.Fatalf(":offline = %v", values[":offline"])
	}
	if b, ok := values[":t"].(*types.AttributeValueMemberBOOL); !ok || !b.Value {
		t.Fatalf(":t = %v", values[":t"])
	}
	if !strings.Contains(cond, "#status = :eligible") || !strings.Contains(cond, "#status = :free") {
		t.Fatalf("cond must require eligible or free: %q", cond)
	}
	if !strings.Contains(cond, "attribute_not_exists(archived)") || !strings.Contains(cond, "archived = :f") {
		t.Fatalf("cond must reject already-archived: %q", cond)
	}
}

func TestArchiveActive_OneUpdateItem(t *testing.T) {
	var body string
	repo := testDERepo(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		_, _ = w.Write([]byte(`{"Attributes":{"phone_number":{"S":"+260971000001"},"archived":{"BOOL":true},"status":{"S":"offline"}}}`))
	})
	got, err := repo.ArchiveActive(context.Background(), "+260971000001")
	if err != nil {
		t.Fatalf("ArchiveActive: %v", err)
	}
	if got == nil || !got.Archived || string(got.Status) != "offline" {
		t.Fatalf("got %#v", got)
	}
	if !strings.Contains(body, "offline") || !strings.Contains(body, "archived") {
		t.Fatalf("single UpdateItem must set both: %s", body)
	}
	if !strings.Contains(body, "ConditionExpression") {
		t.Fatalf("missing condition: %s", body)
	}
}

func TestArchiveActive_UpdateError(t *testing.T) {
	repo := testDERepo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.Header().Set("X-Amzn-Errortype", "InternalServerError")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"__type":"com.amazonaws.dynamodb.v20120810#InternalServerError","message":"boom"}`))
	})
	_, err := repo.ArchiveActive(context.Background(), "+260971000001")
	if err == nil || errors.Is(err, ErrDEArchiveConflict) {
		t.Fatalf("err = %v, want generic update error", err)
	}
}

func TestArchiveActive_ConditionFailed(t *testing.T) {
	repo := testDERepo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.Header().Set("X-Amzn-Errortype", "ConditionalCheckFailedException")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"The conditional request failed"}`))
	})
	_, err := repo.ArchiveActive(context.Background(), "+260971000001")
	if !errors.Is(err, ErrDEArchiveConflict) {
		t.Fatalf("err = %v, want ErrDEArchiveConflict", err)
	}
}

func TestBuildSetArchivedUpdate_Archive(t *testing.T) {
	expr, values := buildSetArchivedUpdate(true, "2026-09-09T12:00:00Z")
	if expr != "SET archived=:t, archived_at=:now, updated_at=:now" {
		t.Fatalf("expr = %q", expr)
	}
	if b, ok := values[":t"].(*types.AttributeValueMemberBOOL); !ok || !b.Value {
		t.Fatalf(":t = %v, want BOOL true", values[":t"])
	}
	if s, ok := values[":now"].(*types.AttributeValueMemberS); !ok || s.Value != "2026-09-09T12:00:00Z" {
		t.Fatalf(":now = %v", values[":now"])
	}
	if _, ok := values[":f"]; ok {
		t.Fatal("archive write must not set :f")
	}
}

func TestBuildSetArchivedUpdate_Restore(t *testing.T) {
	expr, values := buildSetArchivedUpdate(false, "2026-09-09T12:00:00Z")
	if expr != "SET archived=:f, updated_at=:now REMOVE archived_at" {
		t.Fatalf("expr = %q", expr)
	}
	if b, ok := values[":f"].(*types.AttributeValueMemberBOOL); !ok || b.Value {
		t.Fatalf(":f = %v, want BOOL false", values[":f"])
	}
	if _, ok := values[":t"]; ok {
		t.Fatal("restore write must not set :t")
	}
	if strings.Contains(expr, "archived_at=:") {
		t.Fatal("restore must REMOVE archived_at, not SET it")
	}
}
