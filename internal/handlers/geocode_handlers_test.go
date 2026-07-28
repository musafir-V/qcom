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

type stubGeocoder struct {
	res service.AddressLineResult
	err error
}

func (s stubGeocoder) ReverseGeocodeAddressLine(_ context.Context, _, _ float64) (service.AddressLineResult, error) {
	return s.res, s.err
}

func newGeocodeReq(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/geocode/reverse", strings.NewReader(body))
	ctx := context.WithValue(r.Context(), "entity_type", "guest")
	return r.WithContext(ctx)
}

func TestGeocodeReverse(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(io.Discard)

	t.Run("missing latitude is 400", func(t *testing.T) {
		h := NewGeocodeHandlers(stubGeocoder{}, logger)
		w := httptest.NewRecorder()
		h.ReverseGeocode(w, newGeocodeReq(`{"longitude":28.3}`))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("code = %d, want 400", w.Code)
		}
	})

	t.Run("success returns composed line", func(t *testing.T) {
		h := NewGeocodeHandlers(stubGeocoder{res: service.AddressLineResult{AddressLine: "Paul Ngozi Road, Munali", Route: "Paul Ngozi Road", Sublocality: "Munali", Locality: "Lusaka"}}, logger)
		w := httptest.NewRecorder()
		h.ReverseGeocode(w, newGeocodeReq(`{"latitude":-15.38,"longitude":28.32}`))
		if w.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200", w.Code)
		}
		var out struct {
			Data service.AddressLineResult `json:"data"`
		}
		json.NewDecoder(w.Body).Decode(&out)
		if out.Data.AddressLine != "Paul Ngozi Road, Munali" {
			t.Errorf("address_line = %q", out.Data.AddressLine)
		}
	})

	t.Run("no result returns 200 empty line", func(t *testing.T) {
		h := NewGeocodeHandlers(stubGeocoder{err: service.ErrNoGeocodeResult}, logger)
		w := httptest.NewRecorder()
		h.ReverseGeocode(w, newGeocodeReq(`{"latitude":-15.38,"longitude":28.32}`))
		if w.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200", w.Code)
		}
		var out struct {
			Data service.AddressLineResult `json:"data"`
		}
		json.NewDecoder(w.Body).Decode(&out)
		if out.Data.AddressLine != "" {
			t.Errorf("address_line = %q, want empty", out.Data.AddressLine)
		}
	})

	t.Run("geocode error returns 502", func(t *testing.T) {
		h := NewGeocodeHandlers(stubGeocoder{err: errors.New("google down")}, logger)
		w := httptest.NewRecorder()
		h.ReverseGeocode(w, newGeocodeReq(`{"latitude":-15.38,"longitude":28.32}`))
		if w.Code != http.StatusBadGateway {
			t.Fatalf("code = %d, want 502", w.Code)
		}
	})
}
