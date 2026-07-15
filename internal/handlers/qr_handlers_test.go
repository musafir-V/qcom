package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

// fakeStore implements the service's qrStore interface.
type fakeStore struct {
	placement *models.QRPlacement
	campaign  *models.QRCampaign
	scans     int
}

func (f *fakeStore) CreateCampaign(context.Context, *models.QRCampaign) error { return nil }
func (f *fakeStore) GetCampaign(context.Context, string) (*models.QRCampaign, error) {
	return f.campaign, nil
}
func (f *fakeStore) ListCampaigns(context.Context) ([]*models.QRCampaign, error) { return nil, nil }
func (f *fakeStore) UpdateCampaign(context.Context, string, *string, *string, *bool) (*models.QRCampaign, error) {
	return f.campaign, nil
}
func (f *fakeStore) PlacementExists(context.Context, string) (bool, error)      { return false, nil }
func (f *fakeStore) CreatePlacement(context.Context, *models.QRPlacement) error { return nil }
func (f *fakeStore) GetPlacement(context.Context, string) (*models.QRPlacement, error) {
	return f.placement, nil
}
func (f *fakeStore) ListPlacements(context.Context, []string) ([]*models.QRPlacement, error) {
	return nil, nil
}
func (f *fakeStore) UpdatePlacement(context.Context, string, *string, *string, *bool) (*models.QRPlacement, error) {
	return f.placement, nil
}
func (f *fakeStore) RecordScan(context.Context, string, models.Platform, bool, bool, string) error {
	f.scans++
	return nil
}
func (f *fakeStore) QueryScanEvents(context.Context, string, string, string) ([]*models.QRScanEvent, error) {
	return nil, nil
}

func TestRedirect_Android302(t *testing.T) {
	store := &fakeStore{
		placement: &models.QRPlacement{Slug: "abc", CampaignID: "QC1", Enabled: true},
		campaign:  &models.QRCampaign{CampaignID: "QC1", Enabled: true},
	}
	svc := service.NewMarketingQRService(store, logrus.New())
	h := NewQRHandlers(svc, logrus.New())

	req := httptest.NewRequest(http.MethodGet, "/q/abc", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 14)")
	req = mux.SetURLVars(req, map[string]string{"slug": "abc"})
	rec := httptest.NewRecorder()

	h.Redirect(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != service.PlayStoreURL {
		t.Fatalf("expected play store, got %q", loc)
	}
	if store.scans != 1 {
		t.Fatalf("expected 1 scan recorded, got %d", store.scans)
	}
	// First scan sets the uniqueness cookie.
	if len(rec.Result().Cookies()) == 0 {
		t.Fatal("expected uniqueness cookie to be set")
	}
}

func TestRedirect_UnknownSlugFallsBack(t *testing.T) {
	store := &fakeStore{placement: nil}
	svc := service.NewMarketingQRService(store, logrus.New())
	h := NewQRHandlers(svc, logrus.New())

	req := httptest.NewRequest(http.MethodGet, "/q/nope", nil)
	req = mux.SetURLVars(req, map[string]string{"slug": "nope"})
	rec := httptest.NewRecorder()

	h.Redirect(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != service.WebFallbackURL {
		t.Fatalf("expected fallback redirect, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if store.scans != 0 {
		t.Fatal("unknown slug must not record a scan")
	}
}
