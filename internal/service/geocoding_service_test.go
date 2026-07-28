package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirupsen/logrus"
)

// ── mocks ────────────────────────────────────────────────────────────────

// recordingGeocoder is an inner Geocoder that records call counts and lets the
// test stub the response.
type recordingGeocoder struct {
	calls int
	addr  string
	err   error
}

func (g *recordingGeocoder) ReverseGeocode(_ context.Context, _, _ float64) (string, error) {
	g.calls++
	return g.addr, g.err
}

// mockGeocodeCacheStore lets tests stub Get behaviour and observe Save calls
// without persisting state across operations.
type mockGeocodeCacheStore struct {
	getResult *string
	getErr    error
	saveErr   error

	savedCells     []string
	savedAddresses []string
}

func (m *mockGeocodeCacheStore) Get(_ context.Context, _ string) (*string, error) {
	return m.getResult, m.getErr
}

func (m *mockGeocodeCacheStore) Save(_ context.Context, h3Cell string, address string) error {
	m.savedCells = append(m.savedCells, h3Cell)
	m.savedAddresses = append(m.savedAddresses, address)
	return m.saveErr
}

// writeThroughGeocodeStore is an in-memory GeocodeCacheStore that behaves like
// a real cache: a Save immediately makes the value available to subsequent
// Gets. Used to exercise cell-sharing behaviour across calls.
type writeThroughGeocodeStore struct {
	cells map[string]string
}

func newWriteThroughGeocodeStore() *writeThroughGeocodeStore {
	return &writeThroughGeocodeStore{cells: make(map[string]string)}
}

func (s *writeThroughGeocodeStore) Get(_ context.Context, h3Cell string) (*string, error) {
	if v, ok := s.cells[h3Cell]; ok {
		return &v, nil
	}
	return nil, nil
}

func (s *writeThroughGeocodeStore) Save(_ context.Context, h3Cell string, address string) error {
	s.cells[h3Cell] = address
	return nil
}

func newTestCachedGeocoder(inner Geocoder, cache GeocodeCacheStore) *CachedGeocoder {
	return NewCachedGeocoder(inner, cache, logrus.New())
}

// ── cache-hit / cache-miss ────────────────────────────────────────────────

// A1 — cache hit: address comes from cache, inner geocoder is not called.
func TestCachedGeocoder_CacheHit(t *testing.T) {
	cached := "Indiranagar, Bengaluru"
	cache := &mockGeocodeCacheStore{getResult: &cached}
	inner := &recordingGeocoder{addr: "must-not-be-returned"}

	got, err := newTestCachedGeocoder(inner, cache).ReverseGeocode(context.Background(), 12.975, 77.640)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != cached {
		t.Fatalf("expected %q from cache, got %q", cached, got)
	}
	if inner.calls != 0 {
		t.Fatalf("cache hit must not call inner geocoder (got %d calls)", inner.calls)
	}
	if len(cache.savedCells) != 0 {
		t.Fatal("cache hit must not trigger a Save")
	}
}

// A2 — cache miss: inner is called and the result is persisted.
func TestCachedGeocoder_CacheMiss_SavesAndReturns(t *testing.T) {
	cache := &mockGeocodeCacheStore{} // empty → miss
	inner := &recordingGeocoder{addr: "Koramangala, Bengaluru"}

	got, err := newTestCachedGeocoder(inner, cache).ReverseGeocode(context.Background(), 12.935, 77.624)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Koramangala, Bengaluru" {
		t.Fatalf("expected inner result, got %q", got)
	}
	if inner.calls != 1 {
		t.Fatalf("expected 1 inner call on cache miss, got %d", inner.calls)
	}
	if len(cache.savedAddresses) != 1 || cache.savedAddresses[0] != "Koramangala, Bengaluru" {
		t.Fatalf("expected the geocoded address to be saved, got %v", cache.savedAddresses)
	}
	if cache.savedCells[0] == "" {
		t.Fatal("H3 cell key saved to cache must not be empty")
	}
}

// A3 — cache Get error: warns and falls back to inner.
func TestCachedGeocoder_CacheGetError_FallsBackToInner(t *testing.T) {
	cache := &mockGeocodeCacheStore{getErr: errors.New("dynamo unavailable")}
	inner := &recordingGeocoder{addr: "Indiranagar, Bengaluru"}

	got, err := newTestCachedGeocoder(inner, cache).ReverseGeocode(context.Background(), 12.975, 77.640)
	if err != nil {
		t.Fatalf("cache error must not propagate: %v", err)
	}
	if got != "Indiranagar, Bengaluru" {
		t.Fatalf("expected inner result on cache error, got %q", got)
	}
	if inner.calls != 1 {
		t.Fatalf("expected fallback to inner, got %d calls", inner.calls)
	}
}

// A4 — cache Save error: still returns the geocoded address.
func TestCachedGeocoder_SaveError_StillReturnsAddress(t *testing.T) {
	cache := &mockGeocodeCacheStore{saveErr: errors.New("dynamo write failed")}
	inner := &recordingGeocoder{addr: "Indiranagar, Bengaluru"}

	got, err := newTestCachedGeocoder(inner, cache).ReverseGeocode(context.Background(), 12.975, 77.640)
	if err != nil {
		t.Fatalf("save error must not propagate: %v", err)
	}
	if got != "Indiranagar, Bengaluru" {
		t.Fatalf("expected geocoded address, got %q", got)
	}
}

// A5 — inner geocoder error propagates after a cache miss.
func TestCachedGeocoder_InnerError_Propagates(t *testing.T) {
	cache := &mockGeocodeCacheStore{}
	inner := &recordingGeocoder{err: ErrNoGeocodeResult}

	_, err := newTestCachedGeocoder(inner, cache).ReverseGeocode(context.Background(), 12.975, 77.640)
	if !errors.Is(err, ErrNoGeocodeResult) {
		t.Fatalf("expected ErrNoGeocodeResult from inner, got %v", err)
	}
	if len(cache.savedAddresses) != 0 {
		t.Fatal("must not cache when inner returned an error")
	}
}

// ── H3 cell behaviour ────────────────────────────────────────────────────

// A6 — two coordinates inside the same H3 r=9 cell share a cache entry: the
// second call returns the cached value without invoking the inner geocoder.
func TestCachedGeocoder_SameCellUsesCache(t *testing.T) {
	cache := newWriteThroughGeocodeStore()
	inner := &recordingGeocoder{addr: "Indiranagar, Bengaluru"}
	g := newTestCachedGeocoder(inner, cache)

	if _, err := g.ReverseGeocode(context.Background(), 12.97500, 77.64000); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("first call should hit inner once, got %d", inner.calls)
	}

	// ~1 m away — guaranteed inside the same H3 r=9 cell (~170 m hex).
	if _, err := g.ReverseGeocode(context.Background(), 12.97500001, 77.64000001); err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("second call must hit cache, not inner (got %d total calls)", inner.calls)
	}
}

// A7 — coordinates in different H3 r=9 cells must not share a cache entry.
func TestCachedGeocoder_DifferentCellsAreSeparate(t *testing.T) {
	cache := newWriteThroughGeocodeStore()
	inner := &recordingGeocoder{addr: "ignored"}
	g := newTestCachedGeocoder(inner, cache)

	if _, err := g.ReverseGeocode(context.Background(), 12.975, 77.640); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	// ~1 km away — different r=9 cell.
	if _, err := g.ReverseGeocode(context.Background(), 12.985, 77.650); err != nil {
		t.Fatalf("second call failed: %v", err)
	}

	if inner.calls != 2 {
		t.Fatalf("different cells must each miss the cache, got %d inner calls", inner.calls)
	}
	if len(cache.cells) != 2 {
		t.Fatalf("expected 2 distinct cells in cache, got %d", len(cache.cells))
	}
}

// A8 — same coordinates produce a deterministic cell key.
func TestCachedGeocoder_CellKeyDeterministic(t *testing.T) {
	cache1 := &mockGeocodeCacheStore{}
	cache2 := &mockGeocodeCacheStore{}

	_, _ = newTestCachedGeocoder(&recordingGeocoder{addr: "x"}, cache1).
		ReverseGeocode(context.Background(), 12.975, 77.640)
	_, _ = newTestCachedGeocoder(&recordingGeocoder{addr: "x"}, cache2).
		ReverseGeocode(context.Background(), 12.975, 77.640)

	if len(cache1.savedCells) == 0 || len(cache2.savedCells) == 0 {
		t.Fatal("expected cells to be saved on both calls")
	}
	if cache1.savedCells[0] != cache2.savedCells[0] {
		t.Fatalf("H3 cell must be deterministic: %s vs %s", cache1.savedCells[0], cache2.savedCells[0])
	}
}

// ── extractRouteAddressLine ──────────────────────────────────────────────

func TestExtractRouteAddressLine(t *testing.T) {
	comp := func(name string, types ...string) struct {
		LongName string   `json:"long_name"`
		Types    []string `json:"types"`
	} {
		return struct {
			LongName string   `json:"long_name"`
			Types    []string `json:"types"`
		}{LongName: name, Types: types}
	}
	type result = struct {
		FormattedAddress  string `json:"formatted_address"`
		AddressComponents []struct {
			LongName string   `json:"long_name"`
			Types    []string `json:"types"`
		} `json:"address_components"`
	}

	tests := []struct {
		name string
		body googleGeocodeResponse
		want string
	}{
		{
			name: "route and sublocality present",
			body: googleGeocodeResponse{Status: "OK", Results: []result{{
				FormattedAddress: "Plot 542 Paul Ngozi Road, Lusaka",
				AddressComponents: []struct {
					LongName string   `json:"long_name"`
					Types    []string `json:"types"`
				}{comp("Paul Ngozi Road", "route"), comp("Munali", "political", "sublocality", "sublocality_level_1"), comp("Lusaka", "locality")},
			}}},
			want: "Paul Ngozi Road, Munali",
		},
		{
			name: "no route falls back to sublocality",
			body: googleGeocodeResponse{Status: "OK", Results: []result{{
				FormattedAddress: "J826+PM8, Lusaka, Zambia",
				AddressComponents: []struct {
					LongName string   `json:"long_name"`
					Types    []string `json:"types"`
				}{comp("Munali", "sublocality_level_1"), comp("Lusaka", "locality")},
			}}},
			want: "Munali",
		},
		{
			name: "route in a later result is still found",
			body: googleGeocodeResponse{Status: "OK", Results: []result{
				{FormattedAddress: "J82P+92 Lusaka, Zambia", AddressComponents: []struct {
					LongName string   `json:"long_name"`
					Types    []string `json:"types"`
				}{comp("Lusaka", "locality")}},
				{FormattedAddress: "Nangwenya Rd, Lusaka", AddressComponents: []struct {
					LongName string   `json:"long_name"`
					Types    []string `json:"types"`
				}{comp("Nangwenya Road", "route"), comp("Munali", "sublocality_level_1"), comp("Lusaka", "locality")}},
			}},
			want: "Nangwenya Road, Munali",
		},
		{
			name: "only locality falls back to locality",
			body: googleGeocodeResponse{Status: "OK", Results: []result{{
				FormattedAddress: "Lusaka, Zambia",
				AddressComponents: []struct {
					LongName string   `json:"long_name"`
					Types    []string `json:"types"`
				}{comp("Lusaka", "locality")},
			}}},
			want: "Lusaka",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractRouteAddressLine(tt.body)
			if got.AddressLine != tt.want {
				t.Errorf("AddressLine = %q, want %q", got.AddressLine, tt.want)
			}
		})
	}
}

// ── ReverseGeocodeAddressLine ────────────────────────────────────────────

func TestReverseGeocodeAddressLine(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(io.Discard)

	t.Run("not configured returns ErrGeocoderNotConfigured", func(t *testing.T) {
		g := NewGoogleGeocoder("", logger)
		_, err := g.ReverseGeocodeAddressLine(context.Background(), -15.38, 28.32)
		if !errors.Is(err, ErrGeocoderNotConfigured) {
			t.Fatalf("err = %v, want ErrGeocoderNotConfigured", err)
		}
	})

	t.Run("composes route and sublocality", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("result_type"); got != "" {
				t.Errorf("result_type should be unset to include route, got %q", got)
			}
			w.Write([]byte(`{"status":"OK","results":[{"formatted_address":"Plot 542 Paul Ngozi Road, Lusaka","address_components":[{"long_name":"Paul Ngozi Road","types":["route"]},{"long_name":"Munali","types":["sublocality_level_1"]},{"long_name":"Lusaka","types":["locality"]}]}]}`))
		}))
		defer srv.Close()

		g := NewGoogleGeocoder("test-key", logger)
		g.geocodeURL = srv.URL
		got, err := g.ReverseGeocodeAddressLine(context.Background(), -15.38, 28.32)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.AddressLine != "Paul Ngozi Road, Munali" {
			t.Errorf("AddressLine = %q, want %q", got.AddressLine, "Paul Ngozi Road, Munali")
		}
	})

	t.Run("zero results returns ErrNoGeocodeResult", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"status":"ZERO_RESULTS","results":[]}`))
		}))
		defer srv.Close()

		g := NewGoogleGeocoder("test-key", logger)
		g.geocodeURL = srv.URL
		_, err := g.ReverseGeocodeAddressLine(context.Background(), 0.1, 0.1)
		if !errors.Is(err, ErrNoGeocodeResult) {
			t.Fatalf("err = %v, want ErrNoGeocodeResult", err)
		}
	})
}
