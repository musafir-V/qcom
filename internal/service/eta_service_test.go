package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

// ── mock ETACacheStore ────────────────────────────────────────────────────

type mockETACacheStore struct {
	getResult *int
	getErr    error
	saveErr   error
	// tracks every Save call so tests can assert on the persisted value
	savedCells   []string
	savedMinutes []int
}

func (m *mockETACacheStore) Get(_ context.Context, _ string) (*int, error) {
	return m.getResult, m.getErr
}

func (m *mockETACacheStore) Save(_ context.Context, h3Cell string, etaMinutes int) error {
	m.savedCells = append(m.savedCells, h3Cell)
	m.savedMinutes = append(m.savedMinutes, etaMinutes)
	return m.saveErr
}

// writeThroughStore is an in-memory ETACacheStore that behaves like a real
// cache: a Save immediately makes the value available to subsequent Gets.
type writeThroughStore struct {
	cells map[string]int
}

func newWriteThroughStore() *writeThroughStore {
	return &writeThroughStore{cells: make(map[string]int)}
}

func (s *writeThroughStore) Get(_ context.Context, h3Cell string) (*int, error) {
	if v, ok := s.cells[h3Cell]; ok {
		return &v, nil
	}
	return nil, nil
}

func (s *writeThroughStore) Save(_ context.Context, h3Cell string, etaMinutes int) error {
	s.cells[h3Cell] = etaMinutes
	return nil
}

// ── fake Google Distance Matrix server ───────────────────────────────────

// fakeDistanceServer returns an httptest.Server that always replies with a
// successful Distance Matrix response containing distanceMeters.
func fakeDistanceServer(t *testing.T, distanceMeters int) *httptest.Server {
	t.Helper()
	body := fmt.Sprintf(
		`{"status":"OK","rows":[{"elements":[{"status":"OK","distance":{"value":%d}}]}]}`,
		distanceMeters,
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeDistanceServerRaw returns a server that replies with an arbitrary body
// and HTTP status code (for error-path testing).
func fakeDistanceServerRaw(t *testing.T, statusCode int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ── helpers ───────────────────────────────────────────────────────────────

func newTestETAService(t *testing.T, store ETACacheStore, googleURL string) *ETAService {
	t.Helper()
	svc := NewETAService(store, "test-api-key", logrus.New())
	svc.distanceMatrixBaseURL = googleURL
	return svc
}

func testDarkstore() *models.Darkstore {
	return &models.Darkstore{
		DarkstoreID: "DS-001",
		Latitude:    12.975,
		Longitude:   77.640,
	}
}

// ── cache-hit / cache-miss ────────────────────────────────────────────────

// B1 — cache hit: ETA comes from DynamoDB, no Google call.
func TestGetETA_CacheHit(t *testing.T) {
	cached := 7
	store := &mockETACacheStore{getResult: &cached}
	svc := newTestETAService(t, store, "http://must-not-be-called")

	eta, err := svc.GetETA(context.Background(), testDarkstore(), 12.975, 77.640)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eta != 7 {
		t.Fatalf("expected ETA=7 from cache, got %d", eta)
	}
	if len(store.savedMinutes) != 0 {
		t.Fatal("cache hit must not trigger a Save")
	}
}

// B2 — cache miss with Get error: warns and falls back to Google.
func TestGetETA_CacheGetError_FallsBackToGoogle(t *testing.T) {
	srv := fakeDistanceServer(t, 2000) // 2 km → 7 min
	store := &mockETACacheStore{getErr: errors.New("dynamo unavailable")}
	svc := newTestETAService(t, store, srv.URL)

	eta, err := svc.GetETA(context.Background(), testDarkstore(), 12.975, 77.640)
	if err != nil {
		t.Fatalf("cache error must not propagate: %v", err)
	}
	if eta != 7 {
		t.Fatalf("expected ETA=7 from Google fallback, got %d", eta)
	}
}

// ── ETA formula ───────────────────────────────────────────────────────────

// C1 — 500 m → ceil(0.5 × 2) + 3 = 4 min
func TestGetETA_Formula_HalfKilometer(t *testing.T) {
	srv := fakeDistanceServer(t, 500)
	svc := newTestETAService(t, &mockETACacheStore{}, srv.URL)

	eta, err := svc.GetETA(context.Background(), testDarkstore(), 12.975, 77.640)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eta != 4 {
		t.Fatalf("500m: expected 4, got %d", eta)
	}
}

// C2 — 3 700 m → ceil(3.7 × 2) + 3 = 11 min
func TestGetETA_Formula_FractionalKilometer(t *testing.T) {
	srv := fakeDistanceServer(t, 3700)
	svc := newTestETAService(t, &mockETACacheStore{}, srv.URL)

	eta, err := svc.GetETA(context.Background(), testDarkstore(), 12.975, 77.640)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eta != 11 {
		t.Fatalf("3700m: expected 11, got %d", eta)
	}
}

// C3 — 2 000 m → ceil(2.0 × 2) + 3 = 7 min (exact multiple)
func TestGetETA_Formula_ExactKilometer(t *testing.T) {
	srv := fakeDistanceServer(t, 2000)
	svc := newTestETAService(t, &mockETACacheStore{}, srv.URL)

	eta, err := svc.GetETA(context.Background(), testDarkstore(), 12.975, 77.640)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eta != 7 {
		t.Fatalf("2000m: expected 7, got %d", eta)
	}
}

// C4 — 0 m → 0 + 3 = 3 min (only packaging time when store is co-located)
func TestGetETA_Formula_ZeroDistance(t *testing.T) {
	srv := fakeDistanceServer(t, 0)
	svc := newTestETAService(t, &mockETACacheStore{}, srv.URL)

	eta, err := svc.GetETA(context.Background(), testDarkstore(), 12.975, 77.640)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eta != 3 {
		t.Fatalf("0m: expected 3 (packaging only), got %d", eta)
	}
}

// ── cache save behaviour ─────────────────────────────────────────────────

// D1 — computed ETA is persisted to cache with the correct value and a non-empty cell key.
func TestGetETA_SavesCorrectETA(t *testing.T) {
	// 1 500 m → ceil(1.5 × 2) + 3 = ceil(3.0) + 3 = 6 min
	srv := fakeDistanceServer(t, 1500)
	store := &mockETACacheStore{}
	svc := newTestETAService(t, store, srv.URL)

	eta, err := svc.GetETA(context.Background(), testDarkstore(), 12.975, 77.640)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eta != 6 {
		t.Fatalf("1500m: expected 6, got %d", eta)
	}
	if len(store.savedMinutes) == 0 {
		t.Fatal("expected ETA to be saved to cache after Google call")
	}
	if store.savedMinutes[0] != 6 {
		t.Fatalf("saved ETA: expected 6, got %d", store.savedMinutes[0])
	}
	if store.savedCells[0] == "" {
		t.Fatal("H3 cell key saved to cache must not be empty")
	}
}

// D2 — Save failure is logged and does not prevent returning the computed ETA.
func TestGetETA_SaveFailure_StillReturnsETA(t *testing.T) {
	srv := fakeDistanceServer(t, 2000) // 7 min
	store := &mockETACacheStore{saveErr: errors.New("dynamo write failed")}
	svc := newTestETAService(t, store, srv.URL)

	eta, err := svc.GetETA(context.Background(), testDarkstore(), 12.975, 77.640)
	if err != nil {
		t.Fatalf("save failure must not propagate: %v", err)
	}
	if eta != 7 {
		t.Fatalf("expected ETA=7, got %d", eta)
	}
}

// ── H3 cell properties ────────────────────────────────────────────────────

// E1 — same coordinates always produce the same H3 cell key (deterministic).
func TestGetETA_H3CellDeterministic(t *testing.T) {
	srv := fakeDistanceServer(t, 1000)

	store1 := &mockETACacheStore{}
	newTestETAService(t, store1, srv.URL).GetETA(context.Background(), testDarkstore(), 12.975, 77.640) //nolint:errcheck

	store2 := &mockETACacheStore{}
	newTestETAService(t, store2, srv.URL).GetETA(context.Background(), testDarkstore(), 12.975, 77.640) //nolint:errcheck

	if len(store1.savedCells) == 0 || len(store2.savedCells) == 0 {
		t.Fatal("expected cells to be saved")
	}
	if store1.savedCells[0] != store2.savedCells[0] {
		t.Fatalf("H3 cell must be deterministic: got %s and %s", store1.savedCells[0], store2.savedCells[0])
	}
}

// E2 — coordinates within the same H3 resolution-7 cell share a cache entry;
// the second call returns the cached value without hitting Google again.
func TestGetETA_SameCellUsesCache(t *testing.T) {
	googleCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		googleCalls++
		fmt.Fprint(w, `{"status":"OK","rows":[{"elements":[{"status":"OK","distance":{"value":2000}}]}]}`)
	}))
	t.Cleanup(srv.Close)

	store := newWriteThroughStore()
	svc := newTestETAService(t, store, srv.URL)

	// First call: cache miss → Google
	eta1, err := svc.GetETA(context.Background(), testDarkstore(), 12.9750, 77.6400)
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if googleCalls != 1 {
		t.Fatalf("expected 1 Google call after cache miss, got %d", googleCalls)
	}

	// Second call with coordinates ~1 m away — guaranteed same H3 resolution-7 cell.
	eta2, err := svc.GetETA(context.Background(), testDarkstore(), 12.97500001, 77.64000001)
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if googleCalls != 1 {
		t.Fatalf("second call must use cache, not hit Google again (got %d calls)", googleCalls)
	}
	if eta1 != eta2 {
		t.Fatalf("same cell must return same ETA: %d vs %d", eta1, eta2)
	}
}

// ── API-key / HTTP error paths ────────────────────────────────────────────

// F1 — missing API key returns ErrGeocoderNotConfigured (wrapped).
func TestGetETA_NoAPIKey(t *testing.T) {
	store := &mockETACacheStore{} // cache miss
	svc := NewETAService(store, "", logrus.New())
	svc.distanceMatrixBaseURL = "http://not-called"

	_, err := svc.GetETA(context.Background(), testDarkstore(), 12.975, 77.640)
	if err == nil {
		t.Fatal("expected error when API key is missing")
	}
	if !errors.Is(err, ErrGeocoderNotConfigured) {
		t.Fatalf("expected ErrGeocoderNotConfigured (possibly wrapped), got: %v", err)
	}
}

// F2 — Google returns HTTP 500.
func TestGetETA_GoogleHTTPError(t *testing.T) {
	srv := fakeDistanceServerRaw(t, http.StatusInternalServerError, "")
	svc := newTestETAService(t, &mockETACacheStore{}, srv.URL)

	_, err := svc.GetETA(context.Background(), testDarkstore(), 12.975, 77.640)
	if err == nil {
		t.Fatal("expected error for HTTP 500 from Google")
	}
}

// F3 — Google API returns a non-OK top-level status (REQUEST_DENIED).
func TestGetETA_GoogleAPIStatusError(t *testing.T) {
	srv := fakeDistanceServerRaw(t, http.StatusOK, `{"status":"REQUEST_DENIED","rows":[]}`)
	svc := newTestETAService(t, &mockETACacheStore{}, srv.URL)

	_, err := svc.GetETA(context.Background(), testDarkstore(), 12.975, 77.640)
	if err == nil {
		t.Fatal("expected error for non-OK Google API status")
	}
}

// F4 — Google API returns OK top-level status but element status is NOT_FOUND.
func TestGetETA_GoogleElementStatusError(t *testing.T) {
	body := `{"status":"OK","rows":[{"elements":[{"status":"NOT_FOUND","distance":{"value":0}}]}]}`
	srv := fakeDistanceServerRaw(t, http.StatusOK, body)
	svc := newTestETAService(t, &mockETACacheStore{}, srv.URL)

	_, err := svc.GetETA(context.Background(), testDarkstore(), 12.975, 77.640)
	if err == nil {
		t.Fatal("expected error for non-OK element status")
	}
}

// F5 — Google returns malformed JSON.
func TestGetETA_GoogleBadJSON(t *testing.T) {
	srv := fakeDistanceServerRaw(t, http.StatusOK, "not json at all {{")
	svc := newTestETAService(t, &mockETACacheStore{}, srv.URL)

	_, err := svc.GetETA(context.Background(), testDarkstore(), 12.975, 77.640)
	if err == nil {
		t.Fatal("expected error for malformed JSON response")
	}
}

// ── formatLatLng ──────────────────────────────────────────────────────────

func TestFormatLatLng(t *testing.T) {
	cases := []struct {
		lat, lng float64
		want     string
	}{
		{12.975, 77.640, "12.975,77.64"},   // trailing zero dropped
		{0.0, 0.0, "0,0"},
		{-33.8688, 151.2093, "-33.8688,151.2093"},
		{90.0, 180.0, "90,180"},
		{12.975001, 77.640001, "12.975001,77.640001"},
	}
	for _, tc := range cases {
		got := formatLatLng(tc.lat, tc.lng)
		if got != tc.want {
			t.Errorf("formatLatLng(%v, %v) = %q, want %q", tc.lat, tc.lng, got, tc.want)
		}
	}
}
