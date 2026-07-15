package models

// Platform is the resolved device family for a scan.
type Platform string

const (
	PlatformIOS     Platform = "ios"
	PlatformAndroid Platform = "android"
	PlatformOther   Platform = "other"
)

const (
	QRCampaignPKPrefix  = "QRCAMPAIGN!"
	QRPlacementPKPrefix = "QRPLACEMENT!"
	ScanEventSKPrefix   = "SCAN!"
	metadataSK          = "METADATA"
)

// QRCampaign groups several physical QR placements (e.g. one backlit-box design).
type QRCampaign struct {
	CampaignID     string   `json:"campaign_id" dynamodbav:"campaign_id"`
	Name           string   `json:"name" dynamodbav:"name"`
	Description    string   `json:"description,omitempty" dynamodbav:"description,omitempty"`
	Enabled        bool     `json:"enabled" dynamodbav:"enabled"`
	PlacementSlugs []string `json:"placement_slugs,omitempty" dynamodbav:"placement_slugs,omitempty,stringset"`
	CreatedAt      string   `json:"created_at" dynamodbav:"created_at"`
	UpdatedAt      string   `json:"updated_at" dynamodbav:"updated_at"`
}

func (c *QRCampaign) GetPK() string { return QRCampaignPKPrefix + c.CampaignID }
func (c *QRCampaign) GetSK() string { return metadataSK }

// QRPlacement is one physical placement of a campaign, keyed by its slug.
type QRPlacement struct {
	Slug         string `json:"slug" dynamodbav:"slug"`
	CampaignID   string `json:"campaign_id" dynamodbav:"campaign_id"`
	Name         string `json:"name" dynamodbav:"name"`
	Location     string `json:"location,omitempty" dynamodbav:"location,omitempty"`
	Enabled      bool   `json:"enabled" dynamodbav:"enabled"`
	ScanCount    int64  `json:"scan_count" dynamodbav:"scan_count"`
	UniqueCount  int64  `json:"unique_count" dynamodbav:"unique_count"`
	IOSCount     int64  `json:"ios_count" dynamodbav:"ios_count"`
	AndroidCount int64  `json:"android_count" dynamodbav:"android_count"`
	OtherCount   int64  `json:"other_count" dynamodbav:"other_count"`
	BotCount     int64  `json:"bot_count" dynamodbav:"bot_count"`
	CreatedAt    string `json:"created_at" dynamodbav:"created_at"`
	UpdatedAt    string `json:"updated_at" dynamodbav:"updated_at"`
}

func (p *QRPlacement) GetPK() string { return QRPlacementPKPrefix + p.Slug }
func (p *QRPlacement) GetSK() string { return metadataSK }

// QRScanEvent is a single scan, stored time-ordered under its placement.
type QRScanEvent struct {
	Slug       string   `json:"slug" dynamodbav:"slug"`
	CampaignID string   `json:"campaign_id" dynamodbav:"campaign_id"`
	Platform   Platform `json:"platform" dynamodbav:"platform"`
	IsBot      bool     `json:"is_bot" dynamodbav:"is_bot"`
	UserAgent  string   `json:"user_agent,omitempty" dynamodbav:"user_agent,omitempty"`
	CreatedAt  string   `json:"created_at" dynamodbav:"created_at"`
	TTL        int64    `json:"ttl" dynamodbav:"TTL"`
}

func (e *QRScanEvent) GetPK() string { return QRPlacementPKPrefix + e.Slug }
func (e *QRScanEvent) GetSK() string { return ScanEventSKPrefix + e.CreatedAt + "#" }
