package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/repository"
	"github.com/sirupsen/logrus"
)

// fakeDarkstoreStore implements darkstoreStore for handler tests. Only the
// List* methods are exercised here; the rest satisfy the interface.
type fakeDarkstoreStore struct {
	all []models.Darkstore
	err error
}

func (f *fakeDarkstoreStore) GetByID(context.Context, string) (*models.Darkstore, error) {
	return nil, nil
}
func (f *fakeDarkstoreStore) Create(context.Context, repository.CreateDarkstoreInput) (*models.Darkstore, error) {
	return nil, nil
}
func (f *fakeDarkstoreStore) Update(context.Context, string, repository.UpdateDarkstoreInput) (*models.Darkstore, error) {
	return nil, nil
}
func (f *fakeDarkstoreStore) SetActive(context.Context, string, bool) (*models.Darkstore, error) {
	return nil, nil
}
func (f *fakeDarkstoreStore) ListAll(context.Context) ([]models.Darkstore, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.all, nil
}
func (f *fakeDarkstoreStore) ListActive(context.Context) ([]models.Darkstore, error) {
	if f.err != nil {
		return nil, f.err
	}
	active := make([]models.Darkstore, 0, len(f.all))
	for _, ds := range f.all {
		if ds.IsActive {
			active = append(active, ds)
		}
	}
	return active, nil
}

// listDarkstores runs the handler against a fake store and decodes the
// darkstore_id list from the response body.
func listDarkstores(t *testing.T, store *fakeDarkstoreStore, query string) (int, []string) {
	t.Helper()
	h := &AdminStoreHandlers{darkstoreRepo: store, logger: logrus.New()}

	url := "/api/v1/admin/darkstores"
	if query != "" {
		url += "?" + query
	}
	rec := httptest.NewRecorder()
	h.ListDarkstores(rec, httptest.NewRequest(http.MethodGet, url, nil))

	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}

	var body struct {
		Darkstores []map[string]interface{} `json:"darkstores"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	ids := make([]string, 0, len(body.Darkstores))
	for _, ds := range body.Darkstores {
		id, _ := ds["darkstore_id"].(string)
		ids = append(ids, id)
	}
	return rec.Code, ids
}

func TestListDarkstores(t *testing.T) {
	// Deliberately out of order and mixing active/inactive to exercise both
	// the sort and the active-only default.
	store := &fakeDarkstoreStore{all: []models.Darkstore{
		{DarkstoreID: "003", Name: "C", IsActive: true},
		{DarkstoreID: "001", Name: "A", IsActive: true},
		{DarkstoreID: "002", Name: "B", IsActive: false},
	}}

	t.Run("default returns active only, sorted by id", func(t *testing.T) {
		code, ids := listDarkstores(t, store, "")
		if code != http.StatusOK {
			t.Fatalf("expected 200, got %d", code)
		}
		if got, want := ids, []string{"001", "003"}; !equalStrings(got, want) {
			t.Fatalf("expected %v, got %v", want, got)
		}
	})

	t.Run("all=true includes inactive, sorted by id", func(t *testing.T) {
		code, ids := listDarkstores(t, store, "all=true")
		if code != http.StatusOK {
			t.Fatalf("expected 200, got %d", code)
		}
		if got, want := ids, []string{"001", "002", "003"}; !equalStrings(got, want) {
			t.Fatalf("expected %v, got %v", want, got)
		}
	})

	t.Run("all=true is case-insensitive", func(t *testing.T) {
		_, ids := listDarkstores(t, store, "all=TRUE")
		if got, want := ids, []string{"001", "002", "003"}; !equalStrings(got, want) {
			t.Fatalf("expected %v, got %v", want, got)
		}
	})

	t.Run("unrecognized all value falls back to active-only default", func(t *testing.T) {
		_, ids := listDarkstores(t, store, "all=banana")
		if got, want := ids, []string{"001", "003"}; !equalStrings(got, want) {
			t.Fatalf("expected %v, got %v", want, got)
		}
	})

	t.Run("empty store returns empty array, not null", func(t *testing.T) {
		empty := &fakeDarkstoreStore{all: nil}
		h := &AdminStoreHandlers{darkstoreRepo: empty, logger: logrus.New()}
		rec := httptest.NewRecorder()
		h.ListDarkstores(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/darkstores", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatalf("failed to decode: %v", err)
		}
		if string(raw["darkstores"]) != "[]" {
			t.Fatalf("expected darkstores to be [], got %s", raw["darkstores"])
		}
	})
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParsePolygonLines(t *testing.T) {
	t.Run("empty input returns nil", func(t *testing.T) {
		points, err := parsePolygonLines("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if points != nil {
			t.Fatalf("expected nil points, got %v", points)
		}
	})

	t.Run("whitespace-only input returns nil", func(t *testing.T) {
		points, err := parsePolygonLines("   \n  \n")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if points != nil {
			t.Fatalf("expected nil points, got %v", points)
		}
	})

	t.Run("valid 3-point polygon parses", func(t *testing.T) {
		points, err := parsePolygonLines("12.96,77.62\n12.96,77.66\n12.99,77.66")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(points) != 3 {
			t.Fatalf("expected 3 points, got %d", len(points))
		}
		if points[0].Lat != 12.96 || points[0].Lng != 77.62 {
			t.Fatalf("unexpected first point: %+v", points[0])
		}
	})

	t.Run("fewer than 3 points is an error", func(t *testing.T) {
		if _, err := parsePolygonLines("12.96,77.62\n12.96,77.66"); err == nil {
			t.Fatal("expected error for 2-point polygon")
		}
	})

	t.Run("malformed line is an error", func(t *testing.T) {
		if _, err := parsePolygonLines("12.96,77.62\nnot-a-point\n12.99,77.66"); err == nil {
			t.Fatal("expected error for malformed line")
		}
	})

	t.Run("out-of-range coordinate is an error", func(t *testing.T) {
		if _, err := parsePolygonLines("999,77.62\n12.96,77.66\n12.99,77.66"); err == nil {
			t.Fatal("expected error for out-of-range latitude")
		}
	})
}
