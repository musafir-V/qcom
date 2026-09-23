package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type stubDistanceComputer struct {
	result *service.ComputeDistanceResult
	err    error
	called bool
}

func (s *stubDistanceComputer) Compute(_ context.Context, _, _, _, _ float64, _ string) (*service.ComputeDistanceResult, error) {
	s.called = true
	return s.result, s.err
}

func newDistanceReq(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/internal/v1/distance", strings.NewReader(body))
}

func newDistanceHandler(t *testing.T, computer distanceComputer) *DistanceHandlers {
	t.Helper()
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return NewDistanceHandlers(computer, logger)
}

func TestComputeDistance_Validation(t *testing.T) {
	h := newDistanceHandler(t, &stubDistanceComputer{})

	cases := []struct {
		name string
		body string
		code string
	}{
		{"invalid json", `{`, "INVALID_REQUEST"},
		{"missing origin", `{"destination":{"lat":-15.4,"lng":28.3}}`, "MISSING_FIELD"},
		{"missing dest lng", `{"origin":{"lat":-15.4,"lng":28.3},"destination":{"lat":-15.41}}`, "MISSING_FIELD"},
		{"lat out of range", `{"origin":{"lat":99,"lng":28.3},"destination":{"lat":-15.41,"lng":28.31}}`, "INVALID_COORDINATES"},
		{"lng out of range", `{"origin":{"lat":-15.4,"lng":200},"destination":{"lat":-15.41,"lng":28.31}}`, "INVALID_COORDINATES"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ComputeDistance(w, newDistanceReq(tc.body))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400", w.Code)
			}
			var out ErrorResponse
			if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if out.Error.Code != tc.code {
				t.Fatalf("error.code = %q, want %q", out.Error.Code, tc.code)
			}
		})
	}
}

func TestComputeDistance_Success(t *testing.T) {
	hav := 1.2
	goog := 1.5
	stub := &stubDistanceComputer{result: &service.ComputeDistanceResult{
		HaversineKM: &hav,
		GoogleKM:    &goog,
		Method:      service.DistanceMethodBoth,
	}}
	h := newDistanceHandler(t, stub)

	w := httptest.NewRecorder()
	h.ComputeDistance(w, newDistanceReq(`{"origin":{"lat":-15.4,"lng":28.26},"destination":{"lat":-15.41,"lng":28.3}}`))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 body=%s", w.Code, w.Body.String())
	}
	if !stub.called {
		t.Fatal("expected Compute to be called")
	}
	var out struct {
		Data service.ComputeDistanceResult `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Data.Method != service.DistanceMethodBoth {
		t.Fatalf("method = %q", out.Data.Method)
	}
	if out.Data.HaversineKM == nil || *out.Data.HaversineKM != 1.2 {
		t.Fatalf("haversine_km = %v", out.Data.HaversineKM)
	}
	if out.Data.GoogleKM == nil || *out.Data.GoogleKM != 1.5 {
		t.Fatalf("google_km = %v", out.Data.GoogleKM)
	}
}

func TestComputeDistance_GoogleNoRouteIs400(t *testing.T) {
	h := newDistanceHandler(t, &stubDistanceComputer{err: service.ErrNoRoute})
	w := httptest.NewRecorder()
	h.ComputeDistance(w, newDistanceReq(`{"origin":{"lat":-15.4,"lng":28.26},"destination":{"lat":-15.41,"lng":28.3},"method":"google"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", w.Code)
	}
	var out ErrorResponse
	json.NewDecoder(w.Body).Decode(&out)
	if out.Error.Code != "NO_ROUTE" {
		t.Fatalf("error.code = %q, want NO_ROUTE", out.Error.Code)
	}
}

func TestComputeDistance_GoogleUpstreamIs502(t *testing.T) {
	h := newDistanceHandler(t, &stubDistanceComputer{err: errors.New("distance API unavailable")})
	w := httptest.NewRecorder()
	h.ComputeDistance(w, newDistanceReq(`{"origin":{"lat":-15.4,"lng":28.26},"destination":{"lat":-15.41,"lng":28.3},"method":"google"}`))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", w.Code)
	}
	var out ErrorResponse
	json.NewDecoder(w.Body).Decode(&out)
	if out.Error.Code != "UPSTREAM" {
		t.Fatalf("error.code = %q, want UPSTREAM", out.Error.Code)
	}
}

func TestComputeDistance_InvalidMethodIs400(t *testing.T) {
	h := newDistanceHandler(t, &stubDistanceComputer{err: service.ErrInvalidDistanceMethod})
	w := httptest.NewRecorder()
	h.ComputeDistance(w, newDistanceReq(`{"origin":{"lat":-15.4,"lng":28.26},"destination":{"lat":-15.41,"lng":28.3},"method":"walking"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", w.Code)
	}
	var out ErrorResponse
	json.NewDecoder(w.Body).Decode(&out)
	if out.Error.Code != "INVALID_METHOD" {
		t.Fatalf("error.code = %q, want INVALID_METHOD", out.Error.Code)
	}
}

func TestComputeDistance_ZeroZeroIsValid(t *testing.T) {
	// 0,0 is a real coordinate; omitting the field is what we reject.
	stub := &stubDistanceComputer{result: &service.ComputeDistanceResult{Method: service.DistanceMethodHaversine}}
	h := newDistanceHandler(t, stub)
	w := httptest.NewRecorder()
	h.ComputeDistance(w, newDistanceReq(`{"origin":{"lat":0,"lng":0},"destination":{"lat":0,"lng":0},"method":"haversine"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	if !stub.called {
		t.Fatal("0,0 must be accepted when explicitly sent")
	}
}
