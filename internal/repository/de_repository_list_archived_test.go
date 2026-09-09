package repository

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestBuildArchivedListFilter_DefaultExcludes(t *testing.T) {
	expr, values := buildArchivedListFilter(false)
	if expr != "(attribute_not_exists(archived) OR archived = :f)" {
		t.Fatalf("expr = %q", expr)
	}
	if b, ok := values[":f"].(*types.AttributeValueMemberBOOL); !ok || b.Value {
		t.Fatalf(":f = %v, want BOOL false", values[":f"])
	}
}

func TestBuildArchivedListFilter_IncludeHasNoFilter(t *testing.T) {
	expr, values := buildArchivedListFilter(true)
	if expr != "" {
		t.Fatalf("expr = %q, want empty", expr)
	}
	if len(values) != 0 {
		t.Fatalf("values = %v, want empty", values)
	}
	if strings.Contains(expr, "archived") {
		t.Fatal("include_archived=true must not filter")
	}
}

func TestListByAssignedStore_DefaultSendsExcludeFilter(t *testing.T) {
	var body string
	repo := testDERepo(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		_, _ = w.Write([]byte(`{"Items":[]}`))
	})
	if _, _, err := repo.ListByAssignedStore(context.Background(), "221", "", "", 10, false); err != nil {
		t.Fatalf("ListByAssignedStore: %v", err)
	}
	if !strings.Contains(body, "attribute_not_exists(archived)") {
		t.Fatalf("request missing exclude filter: %s", body)
	}
}

func TestListByAssignedStore_IncludeOmitsFilter(t *testing.T) {
	var body string
	repo := testDERepo(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		_, _ = w.Write([]byte(`{"Items":[]}`))
	})
	if _, _, err := repo.ListByAssignedStore(context.Background(), "221", "", "", 10, true); err != nil {
		t.Fatalf("ListByAssignedStore: %v", err)
	}
	if strings.Contains(body, "FilterExpression") || strings.Contains(body, "attribute_not_exists(archived)") {
		t.Fatalf("include_archived=true must omit filter: %s", body)
	}
}
