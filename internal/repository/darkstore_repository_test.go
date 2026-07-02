package repository

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/models"
)

func TestBuildDarkstoreUpdateExpression_EmptyInput(t *testing.T) {
	expr, values, names, err := buildDarkstoreUpdateExpression(UpdateDarkstoreInput{}, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if expr != "SET updated_at = :updated_at" {
		t.Fatalf("unexpected expr: %q", expr)
	}
	if len(values) != 1 {
		t.Fatalf("expected 1 value, got %d: %v", len(values), values)
	}
	if len(names) != 0 {
		t.Fatalf("expected 0 names, got %d: %v", len(names), names)
	}
}

func TestBuildDarkstoreUpdateExpression_Name(t *testing.T) {
	name := "New Name"
	expr, values, names, err := buildDarkstoreUpdateExpression(UpdateDarkstoreInput{Name: &name}, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(expr, "#name = :name") {
		t.Fatalf("expected expr to contain #name = :name, got %q", expr)
	}
	if names["#name"] != "name" {
		t.Fatalf("expected names[#name] == name, got %q", names["#name"])
	}
	av, ok := values[":name"].(*types.AttributeValueMemberS)
	if !ok || av.Value != "New Name" {
		t.Fatalf("unexpected :name value: %v", values[":name"])
	}
}

func TestBuildDarkstoreUpdateExpression_PolygonNilNotIncluded(t *testing.T) {
	expr, values, _, err := buildDarkstoreUpdateExpression(UpdateDarkstoreInput{}, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(expr, "polygon") {
		t.Fatalf("expected no polygon clause, got %q", expr)
	}
	if _, ok := values[":polygon"]; ok {
		t.Fatal("expected no :polygon value")
	}
}

func TestBuildDarkstoreUpdateExpression_PolygonEmptySlice(t *testing.T) {
	empty := []models.PolygonPoint{}
	expr, values, _, err := buildDarkstoreUpdateExpression(UpdateDarkstoreInput{Polygon: &empty}, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(expr, "polygon = :polygon") {
		t.Fatalf("expected polygon clause, got %q", expr)
	}
	av, ok := values[":polygon"].(*types.AttributeValueMemberL)
	if !ok {
		t.Fatalf("expected :polygon to be a list, got %T", values[":polygon"])
	}
	if len(av.Value) != 0 {
		t.Fatalf("expected empty list, got %d items", len(av.Value))
	}
}

func TestBuildDarkstoreUpdateExpression_PolygonNilSliceAlsoEmptyList(t *testing.T) {
	var nilSlice []models.PolygonPoint
	_, values, _, err := buildDarkstoreUpdateExpression(UpdateDarkstoreInput{Polygon: &nilSlice}, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	av, ok := values[":polygon"].(*types.AttributeValueMemberL)
	if !ok {
		t.Fatalf("expected :polygon to be a list (not NULL), got %T", values[":polygon"])
	}
	if len(av.Value) != 0 {
		t.Fatalf("expected empty list for nil slice, got %d items", len(av.Value))
	}
}

func TestBuildDarkstoreUpdateExpression_PolygonThreePoints(t *testing.T) {
	points := []models.PolygonPoint{{Lat: 1, Lng: 2}, {Lat: 3, Lng: 4}, {Lat: 5, Lng: 6}}
	_, values, _, err := buildDarkstoreUpdateExpression(UpdateDarkstoreInput{Polygon: &points}, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	av, ok := values[":polygon"].(*types.AttributeValueMemberL)
	if !ok {
		t.Fatalf("expected :polygon to be a list, got %T", values[":polygon"])
	}
	if len(av.Value) != 3 {
		t.Fatalf("expected 3 items, got %d", len(av.Value))
	}
}

func TestBuildDarkstoreUpdateExpression_MultipleFields(t *testing.T) {
	name := "X"
	lat := 12.5
	expr, values, names, err := buildDarkstoreUpdateExpression(UpdateDarkstoreInput{Name: &name, Latitude: &lat}, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(expr, "#name = :name") || !strings.Contains(expr, "latitude = :latitude") {
		t.Fatalf("expected both clauses present, got %q", expr)
	}
	if len(values) != 3 { // updated_at, name, latitude
		t.Fatalf("expected 3 values, got %d: %v", len(values), values)
	}
	if names["#name"] != "name" {
		t.Fatal("expected #name alias present")
	}
}
