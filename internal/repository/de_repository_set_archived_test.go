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
