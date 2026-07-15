package service

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

const (
	AppStoreURL    = "https://apps.apple.com/us/app/bunzo-groceries-more/id6778587902"
	PlayStoreURL   = "https://play.google.com/store/apps/details?id=com.bunzodelivery.app&hl=en_IN"
	WebFallbackURL = "https://bunzodelivery.com"
	PublicBaseURL  = "https://api.bunzodelivery.com"

	slugAlphabet = "23456789abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ" // no ambiguous 0/O/1/l
	slugLength   = 7
)

// qrStore is the subset of *repository.QRRepository used by the service.
type qrStore interface {
	CreateCampaign(ctx context.Context, c *models.QRCampaign) error
	GetCampaign(ctx context.Context, campaignID string) (*models.QRCampaign, error)
	ListCampaigns(ctx context.Context) ([]*models.QRCampaign, error)
	UpdateCampaign(ctx context.Context, campaignID string, name, description *string, enabled *bool) (*models.QRCampaign, error)
	PlacementExists(ctx context.Context, slug string) (bool, error)
	CreatePlacement(ctx context.Context, p *models.QRPlacement) error
	GetPlacement(ctx context.Context, slug string) (*models.QRPlacement, error)
	ListPlacements(ctx context.Context, slugs []string) ([]*models.QRPlacement, error)
	UpdatePlacement(ctx context.Context, slug string, name, location *string, enabled *bool) (*models.QRPlacement, error)
	RecordScan(ctx context.Context, slug string, platform models.Platform, isBot, unique bool, userAgent string) error
	QueryScanEvents(ctx context.Context, slug, fromISO, toISO string) ([]*models.QRScanEvent, error)
}

type MarketingQRService struct {
	repo   qrStore
	logger *logrus.Logger
}

func NewMarketingQRService(repo qrStore, logger *logrus.Logger) *MarketingQRService {
	return &MarketingQRService{repo: repo, logger: logger}
}

func (s *MarketingQRService) PlacementURL(slug string) string { return PublicBaseURL + "/q/" + slug }

// ClassifyUserAgent returns the resolved platform and whether the UA is a known bot/preview crawler.
func (s *MarketingQRService) ClassifyUserAgent(ua string) (models.Platform, bool) {
	l := strings.ToLower(ua)
	isBot := l == "" ||
		strings.Contains(l, "bot") ||
		strings.Contains(l, "crawler") ||
		strings.Contains(l, "spider") ||
		strings.Contains(l, "preview") ||
		strings.Contains(l, "whatsapp") ||
		strings.Contains(l, "facebookexternalhit") ||
		strings.Contains(l, "slackbot") ||
		strings.Contains(l, "telegrambot") ||
		strings.Contains(l, "twitterbot") ||
		strings.Contains(l, "discordbot") ||
		strings.Contains(l, "linkedinbot") ||
		strings.Contains(l, "embedly") ||
		strings.Contains(l, "curl") ||
		strings.Contains(l, "wget") ||
		strings.Contains(l, "python-requests") ||
		strings.Contains(l, "go-http-client")

	var platform models.Platform
	switch {
	case strings.Contains(l, "iphone") || strings.Contains(l, "ipad") || strings.Contains(l, "ipod"):
		platform = models.PlatformIOS
	case strings.Contains(l, "android"):
		platform = models.PlatformAndroid
	default:
		platform = models.PlatformOther
	}
	return platform, isBot
}

func (s *MarketingQRService) ResolveDestination(platform models.Platform) string {
	switch platform {
	case models.PlatformIOS:
		return AppStoreURL
	case models.PlatformAndroid:
		return PlayStoreURL
	default:
		return WebFallbackURL
	}
}

// HandleScan resolves the redirect target and records the scan. It never fails:
// on any lookup/record error it falls back to the web URL and logs.
// visitorCookie is the raw value of the per-slug uniqueness cookie ("" if absent).
func (s *MarketingQRService) HandleScan(ctx context.Context, slug, userAgent, visitorCookie string) (string, bool) {
	op := logging.Start(ctx, s.logger, "MarketingQRService.HandleScan", logrus.Fields{"slug": slug})
	defer op.End()

	platform, isBot := s.ClassifyUserAgent(userAgent)
	dest := s.ResolveDestination(platform)

	placement, err := s.repo.GetPlacement(ctx, slug)
	if err != nil {
		s.logger.WithError(err).WithField("slug", slug).Warn("qr: placement lookup failed; using fallback")
		return WebFallbackURL, false
	}
	if placement == nil || !placement.Enabled {
		return WebFallbackURL, false
	}

	campaign, err := s.repo.GetCampaign(ctx, placement.CampaignID)
	if err != nil {
		s.logger.WithError(err).WithField("slug", slug).Warn("qr: campaign lookup failed; using fallback")
		return WebFallbackURL, false
	}
	if campaign == nil || !campaign.Enabled {
		return WebFallbackURL, false
	}

	unique := visitorCookie == ""
	if err := s.repo.RecordScan(ctx, slug, platform, isBot, unique, userAgent); err != nil {
		s.logger.WithError(err).WithField("slug", slug).Warn("qr: failed to record scan; still redirecting")
		return dest, false
	}
	op.With("platform", string(platform)).With("is_bot", isBot).With("unique", unique)
	return dest, unique
}

func (s *MarketingQRService) CreateCampaign(ctx context.Context, name, description string) (*models.QRCampaign, error) {
	c := &models.QRCampaign{Name: name, Description: description}
	if err := s.repo.CreateCampaign(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *MarketingQRService) ListCampaigns(ctx context.Context) ([]*models.QRCampaign, error) {
	return s.repo.ListCampaigns(ctx)
}

func (s *MarketingQRService) GetCampaignWithPlacements(ctx context.Context, campaignID string) (*models.QRCampaign, []*models.QRPlacement, error) {
	c, err := s.repo.GetCampaign(ctx, campaignID)
	if err != nil {
		return nil, nil, err
	}
	if c == nil {
		return nil, nil, nil
	}
	placements, err := s.repo.ListPlacements(ctx, c.PlacementSlugs)
	if err != nil {
		return nil, nil, err
	}
	return c, placements, nil
}

func (s *MarketingQRService) UpdateCampaign(ctx context.Context, campaignID string, name, description *string, enabled *bool) (*models.QRCampaign, error) {
	return s.repo.UpdateCampaign(ctx, campaignID, name, description, enabled)
}

func (s *MarketingQRService) AddPlacement(ctx context.Context, campaignID, name, location string) (*models.QRPlacement, error) {
	op := logging.Start(ctx, s.logger, "MarketingQRService.AddPlacement", logrus.Fields{"campaign_id": campaignID})
	defer op.End()

	c, err := s.repo.GetCampaign(ctx, campaignID)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, op.Outcome("campaign_not_found", fmt.Errorf("campaign %q not found", campaignID))
	}

	slug, err := s.generateUniqueSlug(ctx)
	if err != nil {
		return nil, op.Fail(err)
	}
	p := &models.QRPlacement{Slug: slug, CampaignID: campaignID, Name: name, Location: location}
	if err := s.repo.CreatePlacement(ctx, p); err != nil {
		return nil, op.Fail(err)
	}
	return p, nil
}

func (s *MarketingQRService) UpdatePlacement(ctx context.Context, slug string, name, location *string, enabled *bool) (*models.QRPlacement, error) {
	return s.repo.UpdatePlacement(ctx, slug, name, location, enabled)
}

func (s *MarketingQRService) generateUniqueSlug(ctx context.Context) (string, error) {
	for attempt := 0; attempt < 10; attempt++ {
		slug, err := randomSlug()
		if err != nil {
			return "", err
		}
		exists, err := s.repo.PlacementExists(ctx, slug)
		if err != nil {
			return "", err
		}
		if !exists {
			return slug, nil
		}
	}
	return "", fmt.Errorf("failed to generate unique slug after 10 attempts")
}

func randomSlug() (string, error) {
	b := make([]byte, slugLength)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("slug rand: %w", err)
	}
	out := make([]byte, slugLength)
	for i := range b {
		out[i] = slugAlphabet[int(b[i])%len(slugAlphabet)]
	}
	return string(out), nil
}

// --- Analytics ---

type DailyBucket struct {
	Date  string `json:"date"` // YYYY-MM-DD (UTC)
	Scans int64  `json:"scans"`
}

type PlacementAnalytics struct {
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	Location     string `json:"location,omitempty"`
	Enabled      bool   `json:"enabled"`
	URL          string `json:"url"`
	ScanCount    int64  `json:"scan_count"`
	UniqueCount  int64  `json:"unique_count"`
	IOSCount     int64  `json:"ios_count"`
	AndroidCount int64  `json:"android_count"`
	OtherCount   int64  `json:"other_count"`
	BotCount     int64  `json:"bot_count"`
}

type AnalyticsResult struct {
	CampaignID   string               `json:"campaign_id"`
	TotalScans   int64                `json:"total_scans"`
	TotalUnique  int64                `json:"total_unique"`
	TotalIOS     int64                `json:"total_ios"`
	TotalAndroid int64                `json:"total_android"`
	TotalOther   int64                `json:"total_other"`
	Placements   []PlacementAnalytics `json:"placements"`
	Daily        []DailyBucket        `json:"daily"` // campaign-wide, non-bot scans per UTC day, within [from,to]
}

func (s *MarketingQRService) Analytics(ctx context.Context, campaignID, fromISO, toISO string) (*AnalyticsResult, error) {
	op := logging.Start(ctx, s.logger, "MarketingQRService.Analytics", logrus.Fields{"campaign_id": campaignID})
	defer op.End()

	c, placements, err := s.GetCampaignWithPlacements(ctx, campaignID)
	if err != nil {
		return nil, op.Fail(err)
	}
	if c == nil {
		return nil, nil
	}

	res := &AnalyticsResult{CampaignID: campaignID}
	dayCounts := map[string]int64{}
	for _, p := range placements {
		res.TotalScans += p.ScanCount
		res.TotalUnique += p.UniqueCount
		res.TotalIOS += p.IOSCount
		res.TotalAndroid += p.AndroidCount
		res.TotalOther += p.OtherCount
		res.Placements = append(res.Placements, PlacementAnalytics{
			Slug: p.Slug, Name: p.Name, Location: p.Location, Enabled: p.Enabled,
			URL:          s.PlacementURL(p.Slug),
			ScanCount:    p.ScanCount,
			UniqueCount:  p.UniqueCount,
			IOSCount:     p.IOSCount,
			AndroidCount: p.AndroidCount,
			OtherCount:   p.OtherCount,
			BotCount:     p.BotCount,
		})

		events, err := s.repo.QueryScanEvents(ctx, p.Slug, fromISO, toISO)
		if err != nil {
			return nil, op.Fail(err)
		}
		for _, e := range events {
			if e.IsBot {
				continue
			}
			day := e.CreatedAt
			if len(day) >= 10 {
				day = day[:10]
			}
			dayCounts[day]++
		}
	}

	// Emit sorted daily buckets across [from,to] inclusive by day.
	res.Daily = buildDailyBuckets(dayCounts, fromISO, toISO)
	return res, nil
}

func buildDailyBuckets(dayCounts map[string]int64, fromISO, toISO string) []DailyBucket {
	from, errF := time.Parse(time.RFC3339, fromISO)
	to, errT := time.Parse(time.RFC3339, toISO)
	if errF != nil || errT != nil {
		// Fallback: emit whatever days we saw, sorted.
		var out []DailyBucket
		for d, n := range dayCounts {
			out = append(out, DailyBucket{Date: d, Scans: n})
		}
		sortBuckets(out)
		return out
	}
	var out []DailyBucket
	for d := from.UTC(); !d.After(to.UTC()); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		out = append(out, DailyBucket{Date: key, Scans: dayCounts[key]})
	}
	return out
}

func sortBuckets(b []DailyBucket) {
	for i := 1; i < len(b); i++ {
		for j := i; j > 0 && b[j-1].Date > b[j].Date; j-- {
			b[j-1], b[j] = b[j], b[j-1]
		}
	}
}
