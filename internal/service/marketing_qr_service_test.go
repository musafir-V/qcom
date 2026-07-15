package service

import (
	"context"
	"testing"

	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type fakeQRStore struct {
	placement *models.QRPlacement
	campaign  *models.QRCampaign
	recorded  []recordCall
}

type recordCall struct {
	slug     string
	platform models.Platform
	isBot    bool
	unique   bool
}

func (f *fakeQRStore) CreateCampaign(context.Context, *models.QRCampaign) error { return nil }
func (f *fakeQRStore) GetCampaign(_ context.Context, id string) (*models.QRCampaign, error) {
	return f.campaign, nil
}
func (f *fakeQRStore) ListCampaigns(context.Context) ([]*models.QRCampaign, error) { return nil, nil }
func (f *fakeQRStore) UpdateCampaign(context.Context, string, *string, *string, *bool) (*models.QRCampaign, error) {
	return f.campaign, nil
}
func (f *fakeQRStore) PlacementExists(context.Context, string) (bool, error)      { return false, nil }
func (f *fakeQRStore) CreatePlacement(context.Context, *models.QRPlacement) error { return nil }
func (f *fakeQRStore) GetPlacement(_ context.Context, slug string) (*models.QRPlacement, error) {
	return f.placement, nil
}
func (f *fakeQRStore) ListPlacements(context.Context, []string) ([]*models.QRPlacement, error) {
	return nil, nil
}
func (f *fakeQRStore) UpdatePlacement(context.Context, string, *string, *string, *bool) (*models.QRPlacement, error) {
	return f.placement, nil
}
func (f *fakeQRStore) RecordScan(_ context.Context, slug string, p models.Platform, isBot, unique bool, _ string) error {
	f.recorded = append(f.recorded, recordCall{slug, p, isBot, unique})
	return nil
}
func (f *fakeQRStore) QueryScanEvents(context.Context, string, string, string) ([]*models.QRScanEvent, error) {
	return nil, nil
}

func newSvc(store qrStore) *MarketingQRService {
	return NewMarketingQRService(store, logrus.New())
}

func TestClassifyUserAgent(t *testing.T) {
	s := newSvc(&fakeQRStore{})
	cases := []struct {
		ua       string
		platform models.Platform
		bot      bool
	}{
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)", models.PlatformIOS, false},
		{"Mozilla/5.0 (Linux; Android 14; Pixel 8)", models.PlatformAndroid, false},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64)", models.PlatformOther, false},
		{"WhatsApp/2.23", models.PlatformOther, true},
		{"facebookexternalhit/1.1", models.PlatformOther, true},
		{"", models.PlatformOther, true},
	}
	for _, c := range cases {
		p, bot := s.ClassifyUserAgent(c.ua)
		if p != c.platform || bot != c.bot {
			t.Errorf("ClassifyUserAgent(%q) = (%s,%v), want (%s,%v)", c.ua, p, bot, c.platform, c.bot)
		}
	}
}

func TestResolveDestination(t *testing.T) {
	s := newSvc(&fakeQRStore{})
	if s.ResolveDestination(models.PlatformIOS) != AppStoreURL {
		t.Error("ios should resolve to app store")
	}
	if s.ResolveDestination(models.PlatformAndroid) != PlayStoreURL {
		t.Error("android should resolve to play store")
	}
	if s.ResolveDestination(models.PlatformOther) != WebFallbackURL {
		t.Error("other should resolve to web fallback")
	}
}

func TestHandleScan_DisabledPlacementFallsBack(t *testing.T) {
	store := &fakeQRStore{
		placement: &models.QRPlacement{Slug: "abc", CampaignID: "QC1", Enabled: false},
		campaign:  &models.QRCampaign{CampaignID: "QC1", Enabled: true},
	}
	s := newSvc(store)
	dest, unique := s.HandleScan(context.Background(), "abc", "iPhone", "")
	if dest != WebFallbackURL {
		t.Errorf("disabled placement should fall back, got %q", dest)
	}
	if unique {
		t.Error("no unique cookie for disabled placement")
	}
	if len(store.recorded) != 0 {
		t.Error("disabled placement should not record a scan")
	}
}

func TestHandleScan_RecordsAndRedirects(t *testing.T) {
	store := &fakeQRStore{
		placement: &models.QRPlacement{Slug: "abc", CampaignID: "QC1", Enabled: true},
		campaign:  &models.QRCampaign{CampaignID: "QC1", Enabled: true},
	}
	s := newSvc(store)
	dest, unique := s.HandleScan(context.Background(), "abc", "Android", "")
	if dest != PlayStoreURL {
		t.Errorf("android should redirect to play store, got %q", dest)
	}
	if !unique {
		t.Error("first scan (no cookie) should be unique")
	}
	if len(store.recorded) != 1 || store.recorded[0].platform != models.PlatformAndroid || store.recorded[0].isBot {
		t.Errorf("unexpected record: %+v", store.recorded)
	}
}

func TestHandleScan_BotStillRedirectsFlaggedNotUnique(t *testing.T) {
	store := &fakeQRStore{
		placement: &models.QRPlacement{Slug: "abc", CampaignID: "QC1", Enabled: true},
		campaign:  &models.QRCampaign{CampaignID: "QC1", Enabled: true},
	}
	s := newSvc(store)
	dest, unique := s.HandleScan(context.Background(), "abc", "WhatsApp/2.23", "")
	if dest != WebFallbackURL {
		t.Errorf("bot UA is 'other' platform → web fallback, got %q", dest)
	}
	if unique {
		t.Error("bot scan must not be counted unique")
	}
	if len(store.recorded) != 1 || !store.recorded[0].isBot {
		t.Errorf("bot scan should be recorded with is_bot=true: %+v", store.recorded)
	}
}

func TestRandomSlug(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		s, err := randomSlug()
		if err != nil {
			t.Fatal(err)
		}
		if len(s) != slugLength {
			t.Fatalf("slug len = %d", len(s))
		}
		seen[s] = true
	}
	if len(seen) < 90 {
		t.Fatalf("slugs not random enough: %d unique of 100", len(seen))
	}
}
