# Dynamic QR Marketing Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let marketing create QR "campaigns" (each grouping several physical placements), download/print each placement's QR, and track scans (total, unique, iOS/Android split) per placement and per campaign; scanning a QR redirects the visitor to the App Store / Play Store / web depending on device.

**Architecture:** New vertical slice in the Go `qcom` service (models → repository → service → handlers → router wiring) plus a new admin section in the Next.js `admin-dashboard`. The printed QR encodes a stable short URL `https://api.bunzodelivery.com/q/{slug}`; a public unauthenticated handler classifies the User-Agent, records the scan, and issues an HTTP 302. The DynamoDB design keys placements by slug (`QRPLACEMENT!{slug}`) so the redirect is a single `GetItem` — **no new GSI, so no DynamoDB table migration is required**.

**Tech Stack:** Go 1.x, gorilla/mux, aws-sdk-go-v2 DynamoDB (single-table), logrus; Next.js 15 / React 19 / TypeScript / Tailwind, `react-qr-code` (already a dependency), `jszip` (new dependency for bulk download).

## Global Constraints

- qcom repo root: `/Users/shivangawasthi/bunzo/qcom`. admin-dashboard repo root: `/Users/shivangawasthi/bunzo/admin-dashboard`. They are **separate git repos**.
- Feature branch in BOTH repos: `feat/dynamic-qr-marketing`.
- Single DynamoDB table, key schema `PK` (HASH) + `SK` (RANGE); TTL attribute is `TTL` (per `scripts/create-table.sh`). **Do not add any GSI.**
- Timestamps are `time.Now().UTC().Format(time.RFC3339)`.
- All timestamps/counters use existing patterns; every repo method wraps work in `logging.Start(ctx, r.logger, "Name", fields)` + `defer op.End()`.
- Redirect targets (hardcoded consts, verbatim):
  - iOS: `https://apps.apple.com/us/app/bunzo-groceries-more/id6778587902`
  - Android: `https://play.google.com/store/apps/details?id=com.bunzodelivery.app&hl=en_IN`
  - Fallback (desktop/other/disabled/not-found): `https://bunzodelivery.com`
- Public redirect base for building printed URLs: `https://api.bunzodelivery.com`.
- Public redirect route path: `GET /q/{slug}` registered at router root (not under `/api/v1`).
- Admin routes under `/api/v1/admin/qr/...`, gated by `authMiddleware.RequireAdminAuth`.
- Scan-event rows carry a `TTL` epoch-seconds attribute ~400 days out (13 months).
- Bots are still redirected but counted only in `bot_count` (excluded from real totals).
- Go tests run with `cd qcom && go test ./...`. Frontend checks: `cd admin-dashboard && npm run typecheck && npm run lint`.
- Do NOT stage or commit the pre-existing untracked/modified files already in the qcom working tree (AGENTS.md, docs/*.md, scripts/*.py, tests/smoke/*, docs/production-infrastructure.md). Only stage files this feature creates/modifies.

---

## Data Model (reference for all tasks)

**Campaign item** — one per campaign:
- `PK = "QRCAMPAIGN!" + campaign_id`, `SK = "METADATA"`
- attrs: `campaign_id` (e.g. `QC0000000001`), `name`, `description`, `enabled` (bool), `placement_slugs` (String Set), `created_at`, `updated_at`

**Placement item** — one per physical placement; **keyed by slug** (redirect hot path):
- `PK = "QRPLACEMENT!" + slug`, `SK = "METADATA"`
- attrs: `slug`, `campaign_id`, `name`, `location`, `enabled` (bool), `scan_count` (N), `unique_count` (N), `ios_count` (N), `android_count` (N), `other_count` (N), `bot_count` (N), `created_at`, `updated_at`

**Scan event item** — one per scan; time-ordered under the placement:
- `PK = "QRPLACEMENT!" + slug`, `SK = "SCAN!" + createdAt + "#" + rand6`
- attrs: `slug`, `campaign_id`, `platform` (`ios`|`android`|`other`), `is_bot` (bool), `user_agent`, `created_at`, `TTL` (N, epoch seconds)

Listing placements of a campaign = read campaign's `placement_slugs` set, then `BatchGetItem` the placement items. Scans-over-time = `Query` PK=`QRPLACEMENT!{slug}` with `begins_with(SK, "SCAN!")` and an SK range on the timestamp.

---

## Task 1: IDs entity + models

**Files:**
- Modify: `qcom/internal/ids/ids.go` (add `QRCampaign` entity type to the `var (...)` block)
- Create: `qcom/internal/models/qr_marketing.go`
- Create: `qcom/internal/models/qr_marketing_test.go`

**Interfaces:**
- Produces:
  - `ids.QRCampaign = ids.EntityType{Prefix: "QC", CounterKey: "COUNTER!QR_CAMPAIGN"}`
  - `models.QRCampaign` struct with `GetPK()/GetSK()`
  - `models.QRPlacement` struct with `GetPK()/GetSK()`, `SyncIndexKeys()` no-op not needed (slug is PK)
  - `models.QRScanEvent` struct with `GetPK()`, `GetSK()`
  - `models.Platform` type with consts `PlatformIOS="ios"`, `PlatformAndroid="android"`, `PlatformOther="other"`
  - `models.ScanEventSKPrefix = "SCAN!"`, `models.QRPlacementPKPrefix = "QRPLACEMENT!"`, `models.QRCampaignPKPrefix = "QRCAMPAIGN!"`

- [ ] **Step 1: Add the ID entity.** In `qcom/internal/ids/ids.go`, add to the `var (...)` block (after `InKindDisbursement`):

```go
	QRCampaign         = EntityType{Prefix: "QC", CounterKey: "COUNTER!QR_CAMPAIGN"}
```

- [ ] **Step 2: Write the models file** `qcom/internal/models/qr_marketing.go`:

```go
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
```

Note: `GetSK()` on scan event is a prefix; the repository appends a random suffix when writing (see Task 2).

- [ ] **Step 3: Write the test** `qcom/internal/models/qr_marketing_test.go`:

```go
package models

import "testing"

func TestQRKeys(t *testing.T) {
	c := &QRCampaign{CampaignID: "QC0000000001"}
	if c.GetPK() != "QRCAMPAIGN!QC0000000001" {
		t.Fatalf("campaign PK = %q", c.GetPK())
	}
	if c.GetSK() != "METADATA" {
		t.Fatalf("campaign SK = %q", c.GetSK())
	}
	p := &QRPlacement{Slug: "9xKq2Ab"}
	if p.GetPK() != "QRPLACEMENT!9xKq2Ab" {
		t.Fatalf("placement PK = %q", p.GetPK())
	}
	e := &QRScanEvent{Slug: "9xKq2Ab", CreatedAt: "2026-07-15T10:00:00Z"}
	if e.GetPK() != "QRPLACEMENT!9xKq2Ab" {
		t.Fatalf("event PK = %q", e.GetPK())
	}
	if got := e.GetSK(); got != "SCAN!2026-07-15T10:00:00Z#" {
		t.Fatalf("event SK = %q", got)
	}
}
```

- [ ] **Step 4: Run tests.** `cd qcom && go test ./internal/models/ ./internal/ids/` → Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git -C qcom add internal/ids/ids.go internal/models/qr_marketing.go internal/models/qr_marketing_test.go
git -C qcom commit -m "feat(qr): add marketing QR models and QC id entity"
```

---

## Task 2: Repository

**Files:**
- Create: `qcom/internal/repository/qr_repository.go`
- Create: `qcom/internal/repository/qr_repository_test.go`

**Interfaces:**
- Consumes: `models.QRCampaign`, `models.QRPlacement`, `models.QRScanEvent`, `ids.QRCampaign`, `ids.NewGenerator`.
- Produces `*repository.QRRepository` with:
  - `NewQRRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *QRRepository`
  - `CreateCampaign(ctx, *models.QRCampaign) error` — generates `CampaignID` via `ids.QRCampaign` if empty; sets timestamps; `enabled=true`; PutItem with `attribute_not_exists(PK)`.
  - `GetCampaign(ctx, campaignID string) (*models.QRCampaign, error)` — nil if not found.
  - `ListCampaigns(ctx) ([]*models.QRCampaign, error)` — Scan with `begins_with(PK, "QRCAMPAIGN!")` and `SK = "METADATA"`.
  - `UpdateCampaign(ctx, campaignID string, name, description *string, enabled *bool) (*models.QRCampaign, error)` — dynamic SET, `attribute_exists(PK)`, `ReturnValues=ALL_NEW`; nil if not found.
  - `PlacementExists(ctx, slug string) (bool, error)` — GetItem on `QRPLACEMENT!{slug}`.
  - `CreatePlacement(ctx, *models.QRPlacement) error` — sets timestamps, `enabled=true`, counters 0; PutItem `attribute_not_exists(PK)`; then `ADD placement_slugs :slug` on the campaign item.
  - `GetPlacement(ctx, slug string) (*models.QRPlacement, error)` — nil if not found.
  - `ListPlacements(ctx, slugs []string) ([]*models.QRPlacement, error)` — BatchGetItem; empty slice if `slugs` empty.
  - `UpdatePlacement(ctx, slug string, name, location *string, enabled *bool) (*models.QRPlacement, error)` — dynamic SET, `attribute_exists(PK)`, `ReturnValues=ALL_NEW`; nil if not found.
  - `RecordScan(ctx, slug string, platform models.Platform, isBot, unique bool, userAgent string) error` — one UpdateItem (ADD counters, conditional `attribute_exists(PK)`) + one PutItem scan event with TTL.
  - `QueryScanEvents(ctx, slug, fromISO, toISO string) ([]*models.QRScanEvent, error)` — Query PK + SK between `SCAN!{from}` and `SCAN!{to}~`.

- [ ] **Step 1: Write the repository** `qcom/internal/repository/qr_repository.go`:

```go
package repository

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/ids"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

// scanEventTTL is how long raw scan-event rows live (~13 months).
const scanEventTTL = 400 * 24 * time.Hour

type QRRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
	idGen     *ids.Generator
}

func NewQRRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *QRRepository {
	return &QRRepository{
		client:    client,
		tableName: tableName,
		logger:    logger,
		idGen:     ids.NewGenerator(client, tableName),
	}
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

func (r *QRRepository) CreateCampaign(ctx context.Context, c *models.QRCampaign) error {
	op := logging.Start(ctx, r.logger, "QRRepository.CreateCampaign", logrus.Fields{"name": c.Name})
	defer op.End()

	if c.CampaignID == "" {
		id, err := r.idGen.NextID(ctx, ids.QRCampaign)
		if err != nil {
			return op.Fail(fmt.Errorf("failed to generate campaign id: %w", err))
		}
		c.CampaignID = id
	}
	now := nowRFC3339()
	c.CreatedAt = now
	c.UpdatedAt = now
	c.Enabled = true

	item, err := attributevalue.MarshalMap(c)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal campaign: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: c.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: c.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(r.tableName),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to create campaign: %w", err))
	}
	op.With("campaign_id", c.CampaignID)
	return nil
}

func (r *QRRepository) GetCampaign(ctx context.Context, campaignID string) (*models.QRCampaign, error) {
	op := logging.Start(ctx, r.logger, "QRRepository.GetCampaign", logrus.Fields{"campaign_id": campaignID})
	defer op.End()

	res, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: models.QRCampaignPKPrefix + campaignID},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get campaign: %w", err))
	}
	if res.Item == nil {
		return nil, nil
	}
	var c models.QRCampaign
	if err := attributevalue.UnmarshalMap(res.Item, &c); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal campaign: %w", err))
	}
	return &c, nil
}

func (r *QRRepository) ListCampaigns(ctx context.Context) ([]*models.QRCampaign, error) {
	op := logging.Start(ctx, r.logger, "QRRepository.ListCampaigns", nil)
	defer op.End()

	res, err := r.client.Scan(ctx, &dynamodb.ScanInput{
		TableName:        aws.String(r.tableName),
		FilterExpression: aws.String("begins_with(PK, :p) AND SK = :sk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":p":  &types.AttributeValueMemberS{Value: models.QRCampaignPKPrefix},
			":sk": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to scan campaigns: %w", err))
	}
	var out []*models.QRCampaign
	for _, it := range res.Items {
		var c models.QRCampaign
		if err := attributevalue.UnmarshalMap(it, &c); err != nil {
			continue
		}
		out = append(out, &c)
	}
	op.With("count", len(out))
	return out, nil
}

func (r *QRRepository) UpdateCampaign(ctx context.Context, campaignID string, name, description *string, enabled *bool) (*models.QRCampaign, error) {
	op := logging.Start(ctx, r.logger, "QRRepository.UpdateCampaign", logrus.Fields{"campaign_id": campaignID})
	defer op.End()

	sets := []string{"updated_at = :now"}
	values := map[string]types.AttributeValue{":now": &types.AttributeValueMemberS{Value: nowRFC3339()}}
	names := map[string]string{}
	if name != nil {
		sets = append(sets, "#name = :name")
		names["#name"] = "name"
		values[":name"] = &types.AttributeValueMemberS{Value: *name}
	}
	if description != nil {
		sets = append(sets, "description = :desc")
		values[":desc"] = &types.AttributeValueMemberS{Value: *description}
	}
	if enabled != nil {
		sets = append(sets, "enabled = :en")
		values[":en"] = &types.AttributeValueMemberBOOL{Value: *enabled}
	}
	expr := "SET " + joinComma(sets)

	in := &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: models.QRCampaignPKPrefix + campaignID},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression:          aws.String(expr),
		ExpressionAttributeValues: values,
		ConditionExpression:       aws.String("attribute_exists(PK)"),
		ReturnValues:              types.ReturnValueAllNew,
	}
	if len(names) > 0 {
		in.ExpressionAttributeNames = names
	}
	res, err := r.client.UpdateItem(ctx, in)
	if err != nil {
		var cond *types.ConditionalCheckFailedException
		if errors.As(err, &cond) {
			return nil, nil
		}
		return nil, op.Fail(fmt.Errorf("failed to update campaign: %w", err))
	}
	var c models.QRCampaign
	if err := attributevalue.UnmarshalMap(res.Attributes, &c); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal campaign: %w", err))
	}
	return &c, nil
}

func (r *QRRepository) PlacementExists(ctx context.Context, slug string) (bool, error) {
	p, err := r.GetPlacement(ctx, slug)
	if err != nil {
		return false, err
	}
	return p != nil, nil
}

func (r *QRRepository) CreatePlacement(ctx context.Context, p *models.QRPlacement) error {
	op := logging.Start(ctx, r.logger, "QRRepository.CreatePlacement", logrus.Fields{"slug": p.Slug, "campaign_id": p.CampaignID})
	defer op.End()

	now := nowRFC3339()
	p.CreatedAt = now
	p.UpdatedAt = now
	p.Enabled = true

	item, err := attributevalue.MarshalMap(p)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal placement: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: p.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: p.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(r.tableName),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to create placement: %w", err))
	}

	// Register slug on the campaign for enumeration.
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: models.QRCampaignPKPrefix + p.CampaignID},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression: aws.String("ADD placement_slugs :s SET updated_at = :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":s":   &types.AttributeValueMemberSS{Value: []string{p.Slug}},
			":now": &types.AttributeValueMemberS{Value: now},
		},
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to register slug on campaign: %w", err))
	}
	return nil
}

func (r *QRRepository) GetPlacement(ctx context.Context, slug string) (*models.QRPlacement, error) {
	op := logging.Start(ctx, r.logger, "QRRepository.GetPlacement", logrus.Fields{"slug": slug})
	defer op.End()

	res, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: models.QRPlacementPKPrefix + slug},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get placement: %w", err))
	}
	if res.Item == nil {
		return nil, nil
	}
	var p models.QRPlacement
	if err := attributevalue.UnmarshalMap(res.Item, &p); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal placement: %w", err))
	}
	return &p, nil
}

func (r *QRRepository) ListPlacements(ctx context.Context, slugs []string) ([]*models.QRPlacement, error) {
	op := logging.Start(ctx, r.logger, "QRRepository.ListPlacements", logrus.Fields{"count": len(slugs)})
	defer op.End()

	if len(slugs) == 0 {
		return []*models.QRPlacement{}, nil
	}
	var out []*models.QRPlacement
	// BatchGetItem caps at 100 keys.
	for start := 0; start < len(slugs); start += 100 {
		end := start + 100
		if end > len(slugs) {
			end = len(slugs)
		}
		keys := make([]map[string]types.AttributeValue, 0, end-start)
		for _, s := range slugs[start:end] {
			keys = append(keys, map[string]types.AttributeValue{
				"PK": &types.AttributeValueMemberS{Value: models.QRPlacementPKPrefix + s},
				"SK": &types.AttributeValueMemberS{Value: "METADATA"},
			})
		}
		res, err := r.client.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{
			RequestItems: map[string]types.KeysAndAttributes{
				r.tableName: {Keys: keys},
			},
		})
		if err != nil {
			return nil, op.Fail(fmt.Errorf("failed to batch get placements: %w", err))
		}
		for _, it := range res.Responses[r.tableName] {
			var p models.QRPlacement
			if err := attributevalue.UnmarshalMap(it, &p); err != nil {
				continue
			}
			out = append(out, &p)
		}
	}
	return out, nil
}

func (r *QRRepository) UpdatePlacement(ctx context.Context, slug string, name, location *string, enabled *bool) (*models.QRPlacement, error) {
	op := logging.Start(ctx, r.logger, "QRRepository.UpdatePlacement", logrus.Fields{"slug": slug})
	defer op.End()

	sets := []string{"updated_at = :now"}
	values := map[string]types.AttributeValue{":now": &types.AttributeValueMemberS{Value: nowRFC3339()}}
	names := map[string]string{}
	if name != nil {
		sets = append(sets, "#name = :name")
		names["#name"] = "name"
		values[":name"] = &types.AttributeValueMemberS{Value: *name}
	}
	if location != nil {
		sets = append(sets, "#loc = :loc")
		names["#loc"] = "location"
		values[":loc"] = &types.AttributeValueMemberS{Value: *location}
	}
	if enabled != nil {
		sets = append(sets, "enabled = :en")
		values[":en"] = &types.AttributeValueMemberBOOL{Value: *enabled}
	}
	expr := "SET " + joinComma(sets)

	in := &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: models.QRPlacementPKPrefix + slug},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression:          aws.String(expr),
		ExpressionAttributeValues: values,
		ConditionExpression:       aws.String("attribute_exists(PK)"),
		ReturnValues:              types.ReturnValueAllNew,
	}
	if len(names) > 0 {
		in.ExpressionAttributeNames = names
	}
	res, err := r.client.UpdateItem(ctx, in)
	if err != nil {
		var cond *types.ConditionalCheckFailedException
		if errors.As(err, &cond) {
			return nil, nil
		}
		return nil, op.Fail(fmt.Errorf("failed to update placement: %w", err))
	}
	var p models.QRPlacement
	if err := attributevalue.UnmarshalMap(res.Attributes, &p); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal placement: %w", err))
	}
	return &p, nil
}

func (r *QRRepository) RecordScan(ctx context.Context, slug string, platform models.Platform, isBot, unique bool, userAgent string) error {
	op := logging.Start(ctx, r.logger, "QRRepository.RecordScan", logrus.Fields{"slug": slug, "platform": platform, "is_bot": isBot})
	defer op.End()

	now := time.Now().UTC()
	nowISO := now.Format(time.RFC3339)

	// Build the counter update. Bots increment only bot_count.
	var addParts []string
	values := map[string]types.AttributeValue{
		":one": &types.AttributeValueMemberN{Value: "1"},
		":now": &types.AttributeValueMemberS{Value: nowISO},
	}
	if isBot {
		addParts = append(addParts, "bot_count :one")
	} else {
		addParts = append(addParts, "scan_count :one")
		switch platform {
		case models.PlatformIOS:
			addParts = append(addParts, "ios_count :one")
		case models.PlatformAndroid:
			addParts = append(addParts, "android_count :one")
		default:
			addParts = append(addParts, "other_count :one")
		}
		if unique {
			addParts = append(addParts, "unique_count :one")
		}
	}
	updateExpr := "ADD " + joinComma(addParts) + " SET updated_at = :now"

	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: models.QRPlacementPKPrefix + slug},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression:          aws.String(updateExpr),
		ExpressionAttributeValues: values,
		ConditionExpression:       aws.String("attribute_exists(PK)"),
	})
	if err != nil {
		var cond *types.ConditionalCheckFailedException
		if errors.As(err, &cond) {
			return op.Outcome("placement_missing", fmt.Errorf("placement %q not found", slug))
		}
		return op.Fail(fmt.Errorf("failed to increment counters: %w", err))
	}

	// Write the raw event (best-effort; log but don't fail redirect flow decisions here).
	ev := &models.QRScanEvent{
		Slug:      slug,
		Platform:  platform,
		IsBot:     isBot,
		UserAgent: userAgent,
		CreatedAt: nowISO,
		TTL:       now.Add(scanEventTTL).Unix(),
	}
	item, err := attributevalue.MarshalMap(ev)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal scan event: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: ev.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: fmt.Sprintf("%s%s#%06d", models.ScanEventSKPrefix, nowISO, rand.Intn(1000000))}
	if _, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	}); err != nil {
		return op.Fail(fmt.Errorf("failed to write scan event: %w", err))
	}
	return nil
}

func (r *QRRepository) QueryScanEvents(ctx context.Context, slug, fromISO, toISO string) ([]*models.QRScanEvent, error) {
	op := logging.Start(ctx, r.logger, "QRRepository.QueryScanEvents", logrus.Fields{"slug": slug})
	defer op.End()

	res, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		KeyConditionExpression: aws.String("PK = :pk AND SK BETWEEN :from AND :to"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":   &types.AttributeValueMemberS{Value: models.QRPlacementPKPrefix + slug},
			":from": &types.AttributeValueMemberS{Value: models.ScanEventSKPrefix + fromISO},
			":to":   &types.AttributeValueMemberS{Value: models.ScanEventSKPrefix + toISO + "~"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to query scan events: %w", err))
	}
	var out []*models.QRScanEvent
	for _, it := range res.Items {
		var e models.QRScanEvent
		if err := attributevalue.UnmarshalMap(it, &e); err != nil {
			continue
		}
		out = append(out, &e)
	}
	op.With("count", len(out))
	return out, nil
}

// joinComma joins parts with ", " (small helper; avoids importing strings for one call site pattern).
func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
```

- [ ] **Step 2: Write a helper test** `qcom/internal/repository/qr_repository_test.go` (pure function, no DynamoDB — matches repo test convention):

```go
package repository

import "testing"

func TestJoinComma(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"a"}, "a"},
		{[]string{"a", "b"}, "a, b"},
		{[]string{"a", "b", "c"}, "a, b, c"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := joinComma(c.in); got != c.want {
			t.Fatalf("joinComma(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 3: Build + test.** `cd qcom && go build ./... && go test ./internal/repository/` → Expected: PASS.

- [ ] **Step 4: Commit.**

```bash
git -C qcom add internal/repository/qr_repository.go internal/repository/qr_repository_test.go
git -C qcom commit -m "feat(qr): add QR marketing repository (campaigns, placements, scans)"
```

---

## Task 3: Service (slug gen, UA classification, orchestration, analytics)

**Files:**
- Create: `qcom/internal/service/marketing_qr_service.go`
- Create: `qcom/internal/service/marketing_qr_service_test.go`

**Interfaces:**
- Consumes: `*repository.QRRepository` (via a narrow interface for testability), `models.*`.
- Produces `*service.MarketingQRService` with:
  - `NewMarketingQRService(repo qrStore, logger *logrus.Logger) *MarketingQRService`
  - Constants: `AppStoreURL`, `PlayStoreURL`, `WebFallbackURL`, `PublicBaseURL = "https://api.bunzodelivery.com"`.
  - `ClassifyUserAgent(ua string) (models.Platform, bool)` — returns platform + isBot.
  - `ResolveDestination(platform models.Platform) string` — store/web URL.
  - `PlacementURL(slug string) string` — `PublicBaseURL + "/q/" + slug`.
  - `CreateCampaign(ctx, name, description string) (*models.QRCampaign, error)`
  - `ListCampaigns(ctx) ([]*models.QRCampaign, error)`
  - `GetCampaignWithPlacements(ctx, campaignID string) (*models.QRCampaign, []*models.QRPlacement, error)` — nil campaign if not found.
  - `UpdateCampaign(ctx, campaignID string, name, description *string, enabled *bool) (*models.QRCampaign, error)`
  - `AddPlacement(ctx, campaignID, name, location string) (*models.QRPlacement, error)` — verifies campaign exists, generates unique slug, creates placement.
  - `UpdatePlacement(ctx, slug string, name, location *string, enabled *bool) (*models.QRPlacement, error)`
  - `HandleScan(ctx, slug, userAgent, visitorCookie string) (destination string, setUniqueCookie bool)` — resolves placement+campaign enabled, records scan, returns 302 target + whether to set the unique cookie. Never returns error (always yields a safe destination).
  - `Analytics(ctx, campaignID, fromISO, toISO string) (*AnalyticsResult, error)` — per-placement counters + daily buckets.
- Types produced:
  - `qrStore` interface (in this file) covering the repo methods used.
  - `AnalyticsResult`, `DailyBucket`, `PlacementAnalytics` structs.

- [ ] **Step 1: Write the service** `qcom/internal/service/marketing_qr_service.go`:

```go
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
```

- [ ] **Step 2: Write the test** `qcom/internal/service/marketing_qr_service_test.go`:

```go
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
func (f *fakeQRStore) PlacementExists(context.Context, string) (bool, error) { return false, nil }
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
	dest, _ := s.HandleScan(context.Background(), "abc", "WhatsApp/2.23", "")
	if dest != WebFallbackURL {
		t.Errorf("bot UA is 'other' platform → web fallback, got %q", dest)
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
```

- [ ] **Step 3: Build + test.** `cd qcom && go test ./internal/service/ -run 'QR|Scan|UserAgent|Destination|Slug'` → Expected: PASS.

- [ ] **Step 4: Commit.**

```bash
git -C qcom add internal/service/marketing_qr_service.go internal/service/marketing_qr_service_test.go
git -C qcom commit -m "feat(qr): add marketing QR service (UA classify, redirect, analytics)"
```

---

## Task 4: Handlers (public redirect + admin CRUD/analytics)

**Files:**
- Create: `qcom/internal/handlers/qr_handlers.go`
- Create: `qcom/internal/handlers/qr_handlers_test.go`

**Interfaces:**
- Consumes: `*service.MarketingQRService`, `mux.Vars`, shared `ErrorResponse`/`ErrorDetail` from `auth_handlers.go`.
- Produces `*handlers.QRHandlers`:
  - `NewQRHandlers(svc *service.MarketingQRService, logger *logrus.Logger) *QRHandlers`
  - `Redirect(w, r)` — public `GET /q/{slug}`: reads UA + cookie `bq_{slug}`, calls `svc.HandleScan`, sets cookie if unique, `http.Redirect(302)`.
  - `CreateCampaign(w, r)` — `POST /admin/qr/campaigns`
  - `ListCampaigns(w, r)` — `GET /admin/qr/campaigns`
  - `GetCampaign(w, r)` — `GET /admin/qr/campaigns/{campaignId}` (campaign + placements, each with `url`)
  - `UpdateCampaign(w, r)` — `PATCH /admin/qr/campaigns/{campaignId}`
  - `AddPlacement(w, r)` — `POST /admin/qr/campaigns/{campaignId}/placements`
  - `UpdatePlacement(w, r)` — `PATCH /admin/qr/campaigns/{campaignId}/placements/{slug}`
  - `Analytics(w, r)` — `GET /admin/qr/campaigns/{campaignId}/analytics?from=&to=`

- [ ] **Step 1: Write handlers** `qcom/internal/handlers/qr_handlers.go`:

```go
package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/qcom/qcom/internal/models"
	"github.com/qcom/qcom/internal/service"
	"github.com/sirupsen/logrus"
)

type QRHandlers struct {
	svc    *service.MarketingQRService
	logger *logrus.Logger
}

func NewQRHandlers(svc *service.MarketingQRService, logger *logrus.Logger) *QRHandlers {
	return &QRHandlers{svc: svc, logger: logger}
}

func (h *QRHandlers) respondJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (h *QRHandlers) respondError(w http.ResponseWriter, status int, code, message string) {
	h.respondJSON(w, status, ErrorResponse{Error: ErrorDetail{Code: code, Message: message}})
}

// Redirect is the public scan endpoint: GET /q/{slug}
func (h *QRHandlers) Redirect(w http.ResponseWriter, r *http.Request) {
	slug := mux.Vars(r)["slug"]
	cookieName := "bq_" + slug
	var cookieVal string
	if c, err := r.Cookie(cookieName); err == nil {
		cookieVal = c.Value
	}

	dest, setUnique := h.svc.HandleScan(r.Context(), slug, r.UserAgent(), cookieVal)
	if setUnique {
		http.SetCookie(w, &http.Cookie{
			Name:     cookieName,
			Value:    "1",
			Path:     "/",
			MaxAge:   int((30 * 24 * time.Hour).Seconds()),
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
		})
	}
	// Prevent intermediary caching of the redirect so counts stay accurate.
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, dest, http.StatusFound)
}

// --- Admin: campaigns ---

type campaignResponse struct {
	*models.QRCampaign
}

func (h *QRHandlers) CreateCampaign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if req.Name == "" {
		h.respondError(w, http.StatusBadRequest, "MISSING_NAME", "name is required")
		return
	}
	c, err := h.svc.CreateCampaign(r.Context(), req.Name, req.Description)
	if err != nil {
		h.logger.WithError(err).Error("qr: create campaign failed")
		h.respondError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create campaign")
		return
	}
	h.respondJSON(w, http.StatusCreated, c)
}

func (h *QRHandlers) ListCampaigns(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListCampaigns(r.Context())
	if err != nil {
		h.logger.WithError(err).Error("qr: list campaigns failed")
		h.respondError(w, http.StatusInternalServerError, "LIST_FAILED", "Failed to list campaigns")
		return
	}
	if list == nil {
		list = []*models.QRCampaign{}
	}
	h.respondJSON(w, http.StatusOK, list)
}

type placementView struct {
	*models.QRPlacement
	URL string `json:"url"`
}

type campaignDetailResponse struct {
	Campaign   *models.QRCampaign `json:"campaign"`
	Placements []placementView    `json:"placements"`
}

func (h *QRHandlers) GetCampaign(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["campaignId"]
	c, placements, err := h.svc.GetCampaignWithPlacements(r.Context(), id)
	if err != nil {
		h.logger.WithError(err).Error("qr: get campaign failed")
		h.respondError(w, http.StatusInternalServerError, "GET_FAILED", "Failed to get campaign")
		return
	}
	if c == nil {
		h.respondError(w, http.StatusNotFound, "NOT_FOUND", "Campaign not found")
		return
	}
	views := make([]placementView, 0, len(placements))
	for _, p := range placements {
		views = append(views, placementView{QRPlacement: p, URL: h.svc.PlacementURL(p.Slug)})
	}
	h.respondJSON(w, http.StatusOK, campaignDetailResponse{Campaign: c, Placements: views})
}

func (h *QRHandlers) UpdateCampaign(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["campaignId"]
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Enabled     *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	c, err := h.svc.UpdateCampaign(r.Context(), id, req.Name, req.Description, req.Enabled)
	if err != nil {
		h.logger.WithError(err).Error("qr: update campaign failed")
		h.respondError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update campaign")
		return
	}
	if c == nil {
		h.respondError(w, http.StatusNotFound, "NOT_FOUND", "Campaign not found")
		return
	}
	h.respondJSON(w, http.StatusOK, c)
}

// --- Admin: placements ---

func (h *QRHandlers) AddPlacement(w http.ResponseWriter, r *http.Request) {
	campaignID := mux.Vars(r)["campaignId"]
	var req struct {
		Name     string `json:"name"`
		Location string `json:"location"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if req.Name == "" {
		h.respondError(w, http.StatusBadRequest, "MISSING_NAME", "name is required")
		return
	}
	p, err := h.svc.AddPlacement(r.Context(), campaignID, req.Name, req.Location)
	if err != nil {
		h.logger.WithError(err).Error("qr: add placement failed")
		h.respondError(w, http.StatusInternalServerError, "ADD_FAILED", "Failed to add placement")
		return
	}
	h.respondJSON(w, http.StatusCreated, placementView{QRPlacement: p, URL: h.svc.PlacementURL(p.Slug)})
}

func (h *QRHandlers) UpdatePlacement(w http.ResponseWriter, r *http.Request) {
	slug := mux.Vars(r)["slug"]
	var req struct {
		Name     *string `json:"name"`
		Location *string `json:"location"`
		Enabled  *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.respondError(w, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request body")
		return
	}
	p, err := h.svc.UpdatePlacement(r.Context(), slug, req.Name, req.Location, req.Enabled)
	if err != nil {
		h.logger.WithError(err).Error("qr: update placement failed")
		h.respondError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update placement")
		return
	}
	if p == nil {
		h.respondError(w, http.StatusNotFound, "NOT_FOUND", "Placement not found")
		return
	}
	h.respondJSON(w, http.StatusOK, placementView{QRPlacement: p, URL: h.svc.PlacementURL(p.Slug)})
}

func (h *QRHandlers) Analytics(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["campaignId"]
	now := time.Now().UTC()
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" {
		from = now.AddDate(0, 0, -30).Format(time.RFC3339)
	}
	if to == "" {
		to = now.Format(time.RFC3339)
	}
	res, err := h.svc.Analytics(r.Context(), id, from, to)
	if err != nil {
		h.logger.WithError(err).Error("qr: analytics failed")
		h.respondError(w, http.StatusInternalServerError, "ANALYTICS_FAILED", "Failed to load analytics")
		return
	}
	if res == nil {
		h.respondError(w, http.StatusNotFound, "NOT_FOUND", "Campaign not found")
		return
	}
	h.respondJSON(w, http.StatusOK, res)
}
```

- [ ] **Step 2: Write handler tests** `qcom/internal/handlers/qr_handlers_test.go` (uses `httptest`, real service backed by a fake store; define the fake store inline here since the service's `qrStore` interface is unexported — instead back the service with a local fake implementing the same methods, exposed via `service.NewMarketingQRService`):

```go
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
func (f *fakeStore) PlacementExists(context.Context, string) (bool, error)     { return false, nil }
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
```

- [ ] **Step 3: Build + test.** `cd qcom && go test ./internal/handlers/ -run 'Redirect'` → Expected: PASS.

- [ ] **Step 4: Commit.**

```bash
git -C qcom add internal/handlers/qr_handlers.go internal/handlers/qr_handlers_test.go
git -C qcom commit -m "feat(qr): add QR redirect + admin campaign/placement/analytics handlers"
```

---

## Task 5: Router wiring

**Files:**
- Modify: `qcom/cmd/server/main.go`

**Interfaces:**
- Consumes: `repository.NewQRRepository`, `service.NewMarketingQRService`, `handlers.NewQRHandlers` (Tasks 2–4).

- [ ] **Step 1: Construct repo/service/handler.** In `main()`, after the referral repo line (`referralRepo := ...`, ~line 53) add:

```go
	qrRepo := repository.NewQRRepository(dynamoClient, cfg.DynamoDB.TableName, logger)
```

After `qrService := service.NewQRService(logger)` (~line 89) add:

```go
	marketingQRService := service.NewMarketingQRService(qrRepo, logger)
```

After `webhookHandlers := handlers.NewWebhookHandlers(logger)` (~line 176) add:

```go
	qrHandlers := handlers.NewQRHandlers(marketingQRService, logger)
```

- [ ] **Step 2: Pass to setupRouter.** In the `router := setupRouter(...)` call (~line 202), add `qrHandlers` before `authMiddleware`:

```go
	router := setupRouter(authHandlers, homeHandlers, uploadHandlers, addressHandlers, serviceabilityHandlers, deHandlers, referralHandlers, configHandlers, tripHandlers, adminHandlers, adminRulesHandlers, adminAuthHandlers, adminDriverHandlers, adminStoreHandlers, trackHandlers, earningsHandlers, disbursementHandlers, cashDepositHandlers, notificationHandlers, webhookHandlers, disputeHandlers, adminDisputeHandlers, voiceHandlers, qrHandlers, authMiddleware, logger)
```

- [ ] **Step 3: Extend setupRouter signature.** Add the parameter before `authMiddleware *middleware.AuthMiddleware` (~line 341):

```go
	voiceHandlers *handlers.VoiceHandlers,
	qrHandlers *handlers.QRHandlers,
	authMiddleware *middleware.AuthMiddleware,
	logger *logrus.Logger,
) *mux.Router {
```

- [ ] **Step 4: Register the public redirect route.** After the `/health` route registration (~line 363), add:

```go
	// Public marketing QR redirect — no auth. Encodes device-aware app-download links.
	router.HandleFunc("/q/{slug}", qrHandlers.Redirect).Methods("GET", "OPTIONS")
```

- [ ] **Step 5: Register admin routes.** Inside the `admin` subrouter block (after the disputes routes, ~line 452), add:

```go
	// Dynamic QR marketing campaigns (admin).
	admin.HandleFunc("/qr/campaigns", qrHandlers.ListCampaigns).Methods("GET", "OPTIONS")
	admin.HandleFunc("/qr/campaigns", qrHandlers.CreateCampaign).Methods("POST", "OPTIONS")
	admin.HandleFunc("/qr/campaigns/{campaignId}/analytics", qrHandlers.Analytics).Methods("GET", "OPTIONS")
	admin.HandleFunc("/qr/campaigns/{campaignId}/placements", qrHandlers.AddPlacement).Methods("POST", "OPTIONS")
	admin.HandleFunc("/qr/campaigns/{campaignId}/placements/{slug}", qrHandlers.UpdatePlacement).Methods("PATCH", "OPTIONS")
	admin.HandleFunc("/qr/campaigns/{campaignId}", qrHandlers.GetCampaign).Methods("GET", "OPTIONS")
	admin.HandleFunc("/qr/campaigns/{campaignId}", qrHandlers.UpdateCampaign).Methods("PATCH", "OPTIONS")
```

(Analytics + placements routes are registered before the generic `/qr/campaigns/{campaignId}` so gorilla/mux does not shadow them.)

- [ ] **Step 6: Build + full test + vet.** `cd qcom && go build ./... && go vet ./... && go test ./...` → Expected: build OK, all tests PASS.

- [ ] **Step 7: Commit.**

```bash
git -C qcom add cmd/server/main.go
git -C qcom commit -m "feat(qr): wire QR redirect + admin routes into router"
```

---

## Task 6: Frontend — types + API client + nav

**Files:**
- Create: `admin-dashboard/src/lib/qrTypes.ts`
- Create: `admin-dashboard/src/lib/qrApi.ts`
- Modify: `admin-dashboard/src/lib/navConfig.ts`

**Interfaces:**
- Consumes: qcom admin endpoints `/admin/qr/...` (direct via `NEXT_PUBLIC_API_BASE_URL`), `getStoredToken` + `useAuth` from `src/lib/store.ts`.
- Produces: `qrApi` object, TS types, nav section.

- [ ] **Step 1: Types** `admin-dashboard/src/lib/qrTypes.ts`:

```ts
export interface QrCampaign {
  campaign_id: string;
  name: string;
  description?: string;
  enabled: boolean;
  placement_slugs?: string[];
  created_at: string;
  updated_at: string;
}

export interface QrPlacement {
  slug: string;
  campaign_id: string;
  name: string;
  location?: string;
  enabled: boolean;
  scan_count: number;
  unique_count: number;
  ios_count: number;
  android_count: number;
  other_count: number;
  bot_count: number;
  created_at: string;
  updated_at: string;
  url: string;
}

export interface QrCampaignDetail {
  campaign: QrCampaign;
  placements: QrPlacement[];
}

export interface QrDailyBucket {
  date: string;
  scans: number;
}

export interface QrPlacementAnalytics {
  slug: string;
  name: string;
  location?: string;
  enabled: boolean;
  url: string;
  scan_count: number;
  unique_count: number;
  ios_count: number;
  android_count: number;
  other_count: number;
  bot_count: number;
}

export interface QrAnalytics {
  campaign_id: string;
  total_scans: number;
  total_unique: number;
  total_ios: number;
  total_android: number;
  total_other: number;
  placements: QrPlacementAnalytics[];
  daily: QrDailyBucket[];
}

export interface CreateCampaignRequest {
  name: string;
  description?: string;
}

export interface UpdateCampaignRequest {
  name?: string;
  description?: string;
  enabled?: boolean;
}

export interface AddPlacementRequest {
  name: string;
  location?: string;
}

export interface UpdatePlacementRequest {
  name?: string;
  location?: string;
  enabled?: boolean;
}
```

- [ ] **Step 2: API client** `admin-dashboard/src/lib/qrApi.ts` (direct-to-qcom pattern mirroring `src/lib/api.ts`; on 401 it logs out via `useAuth`):

```ts
'use client';

import { getStoredToken, useAuth } from './store';
import type {
  QrCampaign,
  QrCampaignDetail,
  QrPlacement,
  QrAnalytics,
  CreateCampaignRequest,
  UpdateCampaignRequest,
  AddPlacementRequest,
  UpdatePlacementRequest
} from './qrTypes';

const BASE = process.env.NEXT_PUBLIC_API_BASE_URL ?? 'https://api.bunzodelivery.com';

export class QrApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = 'QrApiError';
    this.status = status;
  }
}

async function req<T>(path: string, opts: { method?: string; body?: unknown } = {}): Promise<T> {
  const headers: Record<string, string> = {};
  const token = getStoredToken();
  if (token) headers['Authorization'] = `Bearer ${token}`;
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json';

  let res: Response;
  try {
    res = await fetch(`${BASE}/api/v1${path}`, {
      method: opts.method ?? 'GET',
      headers,
      body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined
    });
  } catch {
    throw new QrApiError(0, 'Could not reach the server.');
  }

  if (res.status === 401) {
    useAuth.getState().logout();
    throw new QrApiError(401, 'Your session has expired. Please sign in again.');
  }

  let data: unknown = null;
  const text = await res.text();
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }

  if (!res.ok) {
    const message =
      (data && typeof data === 'object' && 'error' in data &&
        (data as { error?: { message?: string } }).error?.message) ||
      `Request failed (${res.status})`;
    throw new QrApiError(res.status, message as string);
  }
  return data as T;
}

export const qrApi = {
  listCampaigns: () => req<QrCampaign[]>('/admin/qr/campaigns'),
  createCampaign: (body: CreateCampaignRequest) =>
    req<QrCampaign>('/admin/qr/campaigns', { method: 'POST', body }),
  getCampaign: (id: string) =>
    req<QrCampaignDetail>(`/admin/qr/campaigns/${encodeURIComponent(id)}`),
  updateCampaign: (id: string, body: UpdateCampaignRequest) =>
    req<QrCampaign>(`/admin/qr/campaigns/${encodeURIComponent(id)}`, { method: 'PATCH', body }),
  addPlacement: (id: string, body: AddPlacementRequest) =>
    req<QrPlacement>(`/admin/qr/campaigns/${encodeURIComponent(id)}/placements`, { method: 'POST', body }),
  updatePlacement: (id: string, slug: string, body: UpdatePlacementRequest) =>
    req<QrPlacement>(
      `/admin/qr/campaigns/${encodeURIComponent(id)}/placements/${encodeURIComponent(slug)}`,
      { method: 'PATCH', body }
    ),
  analytics: (id: string, from?: string, to?: string) => {
    const qs = new URLSearchParams();
    if (from) qs.set('from', from);
    if (to) qs.set('to', to);
    const suffix = qs.toString() ? `?${qs.toString()}` : '';
    return req<QrAnalytics>(`/admin/qr/campaigns/${encodeURIComponent(id)}/analytics${suffix}`);
  }
};
```

- [ ] **Step 3: Nav.** In `admin-dashboard/src/lib/navConfig.ts`, add a new section to `NAV_SECTIONS` immediately after the `barcode-generator` section object:

```ts
  {
    id: 'qr-campaigns',
    label: 'QR Campaigns',
    items: [
      { href: '/qr-campaigns', label: 'All Campaigns', icon: QrCode, exact: true },
      { href: '/qr-campaigns/new', label: 'Create Campaign', icon: Plus }
    ]
  },
```

(`QrCode` and `Plus` are already imported in this file.)

- [ ] **Step 4: Typecheck.** `cd admin-dashboard && npm run typecheck` → Expected: no errors.

- [ ] **Step 5: Commit.**

```bash
git -C admin-dashboard add src/lib/qrTypes.ts src/lib/qrApi.ts src/lib/navConfig.ts
git -C admin-dashboard commit -m "feat(qr): add QR admin API client, types, and nav section"
```

---

## Task 7: Frontend — QR image util + card component (PNG/print/ZIP)

**Files:**
- Create: `admin-dashboard/src/lib/qrImage.ts`
- Create: `admin-dashboard/src/components/qr/QrCard.tsx`
- Modify: `admin-dashboard/package.json` (add `jszip`)

**Interfaces:**
- Consumes: `react-qr-code` (existing dep), `jszip` (new).
- Produces:
  - `svgElementToPngBlob(svg: SVGSVGElement, size?: number): Promise<Blob>`
  - `downloadBlob(blob: Blob, filename: string): void`
  - `sanitizeFilename(name: string): string`
  - `QrCard` React component: renders a QR for a `QrPlacement`, with Download PNG + Print buttons.

- [ ] **Step 1: Add jszip.** `cd admin-dashboard && npm install jszip@^3.10.1` (this updates package.json + package-lock.json).

- [ ] **Step 2: Image util** `admin-dashboard/src/lib/qrImage.ts`:

```ts
export function sanitizeFilename(name: string): string {
  return name.replace(/[^a-z0-9-_]+/gi, '_').replace(/_+/g, '_').replace(/^_|_$/g, '') || 'qr';
}

export function svgElementToPngBlob(svg: SVGSVGElement, size = 1024): Promise<Blob> {
  return new Promise((resolve, reject) => {
    const svgData = new XMLSerializer().serializeToString(svg);
    const svgBlob = new Blob([svgData], { type: 'image/svg+xml;charset=utf-8' });
    const url = URL.createObjectURL(svgBlob);
    const img = new Image();
    img.onload = () => {
      const canvas = document.createElement('canvas');
      canvas.width = size;
      canvas.height = size;
      const ctx = canvas.getContext('2d');
      if (!ctx) {
        URL.revokeObjectURL(url);
        reject(new Error('canvas 2d context unavailable'));
        return;
      }
      ctx.fillStyle = '#ffffff';
      ctx.fillRect(0, 0, size, size);
      ctx.drawImage(img, 0, 0, size, size);
      URL.revokeObjectURL(url);
      canvas.toBlob((blob) => {
        if (blob) resolve(blob);
        else reject(new Error('canvas toBlob returned null'));
      }, 'image/png');
    };
    img.onerror = () => {
      URL.revokeObjectURL(url);
      reject(new Error('failed to rasterize SVG'));
    };
    img.src = url;
  });
}

export function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}
```

- [ ] **Step 3: QrCard component** `admin-dashboard/src/components/qr/QrCard.tsx`:

```tsx
'use client';

import { useRef } from 'react';
import QRCode from 'react-qr-code';
import { Download, Printer } from 'lucide-react';
import type { QrPlacement } from '@/lib/qrTypes';
import { svgElementToPngBlob, downloadBlob, sanitizeFilename } from '@/lib/qrImage';

export function QrCard({ placement }: { placement: QrPlacement }) {
  const ref = useRef<HTMLDivElement>(null);

  async function handleDownload() {
    const svg = ref.current?.querySelector('svg');
    if (!svg) return;
    const blob = await svgElementToPngBlob(svg as SVGSVGElement, 1024);
    downloadBlob(blob, `${sanitizeFilename(placement.name)}_${placement.slug}.png`);
  }

  return (
    <div className="card flex flex-col items-center gap-3 p-4 print:break-inside-avoid">
      <div ref={ref} className="rounded-lg bg-white p-2">
        <QRCode value={placement.url} size={180} level="M" />
      </div>
      <div className="text-center">
        <div className="text-sm font-semibold text-gray-900">{placement.name}</div>
        {placement.location && <div className="text-xs text-gray-500">{placement.location}</div>}
        <div className="mt-1 break-all text-[10px] text-gray-400">{placement.url}</div>
      </div>
      <div className="flex gap-2 print:hidden">
        <button type="button" onClick={handleDownload} className="btn-ghost flex items-center gap-1.5 text-xs">
          <Download className="h-3.5 w-3.5" /> PNG
        </button>
        <button type="button" onClick={() => window.print()} className="btn-ghost flex items-center gap-1.5 text-xs">
          <Printer className="h-3.5 w-3.5" /> Print
        </button>
      </div>
    </div>
  );
}
```

- [ ] **Step 4: Typecheck + lint.** `cd admin-dashboard && npm run typecheck && npm run lint` → Expected: no errors.

- [ ] **Step 5: Commit.**

```bash
git -C admin-dashboard add package.json package-lock.json src/lib/qrImage.ts src/components/qr/QrCard.tsx
git -C admin-dashboard commit -m "feat(qr): add QR PNG/print util and QrCard component (jszip dep)"
```

---

## Task 8: Frontend — pages (list, create, detail + analytics)

**Files:**
- Create: `admin-dashboard/src/app/qr-campaigns/page.tsx` (list)
- Create: `admin-dashboard/src/app/qr-campaigns/new/page.tsx` (create)
- Create: `admin-dashboard/src/app/qr-campaigns/[id]/page.tsx` (detail: placements + QR cards + analytics)
- Create: `admin-dashboard/src/components/qr/ScanBars.tsx` (dependency-free daily bar chart)

**Interfaces:**
- Consumes: `qrApi`, `qrTypes`, `QrCard`, shared UI from `@/components/ui` (`Card`, `Loading`, `ErrorBox`, `EmptyState`, `Badge`, `Stat`, `useToast`), `jszip`, `svgElementToPngBlob`/`downloadBlob`/`sanitizeFilename`.

- [ ] **Step 1: ScanBars** `admin-dashboard/src/components/qr/ScanBars.tsx`:

```tsx
'use client';

import type { QrDailyBucket } from '@/lib/qrTypes';

export function ScanBars({ data }: { data: QrDailyBucket[] }) {
  if (!data.length) return <div className="text-sm text-gray-400">No scans in this range.</div>;
  const max = Math.max(1, ...data.map((d) => d.scans));
  return (
    <div className="flex h-40 items-end gap-1 overflow-x-auto">
      {data.map((d) => (
        <div key={d.date} className="flex min-w-[10px] flex-1 flex-col items-center gap-1" title={`${d.date}: ${d.scans}`}>
          <div className="text-[9px] text-gray-500">{d.scans || ''}</div>
          <div
            className="w-full rounded-t bg-brand-green"
            style={{ height: `${Math.round((d.scans / max) * 120)}px`, minHeight: d.scans ? '2px' : '0px' }}
          />
          <div className="text-[8px] text-gray-400">{d.date.slice(5)}</div>
        </div>
      ))}
    </div>
  );
}
```

- [ ] **Step 2: List page** `admin-dashboard/src/app/qr-campaigns/page.tsx`:

```tsx
'use client';

import { useCallback, useEffect, useState } from 'react';
import Link from 'next/link';
import { Plus } from 'lucide-react';
import { qrApi } from '@/lib/qrApi';
import type { QrCampaign } from '@/lib/qrTypes';
import { Card, Loading, ErrorBox, EmptyState, Badge } from '@/components/ui';

export default function QrCampaignsPage() {
  const [campaigns, setCampaigns] = useState<QrCampaign[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      setCampaigns(await qrApi.listCampaigns());
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load campaigns');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">QR Campaigns</h1>
          <p className="text-sm text-gray-500">Dynamic QR codes for app-download marketing.</p>
        </div>
        <Link href="/qr-campaigns/new" className="btn-primary flex items-center gap-1.5">
          <Plus className="h-4 w-4" /> Create Campaign
        </Link>
      </div>

      {loading ? (
        <Loading label="Loading campaigns…" />
      ) : error ? (
        <ErrorBox message={error} />
      ) : campaigns.length === 0 ? (
        <EmptyState title="No campaigns yet" description="Create your first QR campaign to get started." />
      ) : (
        <Card className="overflow-hidden p-0">
          <table className="w-full text-sm">
            <thead className="bg-gray-50 text-left text-xs uppercase text-gray-500">
              <tr>
                <th className="px-4 py-3">Name</th>
                <th className="px-4 py-3">Placements</th>
                <th className="px-4 py-3">Status</th>
                <th className="px-4 py-3">Created</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {campaigns.map((c) => (
                <tr key={c.campaign_id} className="hover:bg-gray-50">
                  <td className="px-4 py-3">
                    <Link href={`/qr-campaigns/${c.campaign_id}`} className="font-medium text-brand-green hover:underline">
                      {c.name}
                    </Link>
                    {c.description && <div className="text-xs text-gray-400">{c.description}</div>}
                  </td>
                  <td className="px-4 py-3 text-gray-600">{c.placement_slugs?.length ?? 0}</td>
                  <td className="px-4 py-3">
                    <Badge tone={c.enabled ? 'green' : 'gray'}>{c.enabled ? 'Active' : 'Disabled'}</Badge>
                  </td>
                  <td className="px-4 py-3 text-gray-500">{new Date(c.created_at).toLocaleDateString('en-IN')}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </Card>
      )}
    </div>
  );
}
```

- [ ] **Step 3: Create page** `admin-dashboard/src/app/qr-campaigns/new/page.tsx`:

```tsx
'use client';

import { useState } from 'react';
import { useRouter } from 'next/navigation';
import { qrApi } from '@/lib/qrApi';
import { Card, ErrorBox } from '@/components/ui';

export default function NewQrCampaignPage() {
  const router = useRouter();
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    setSaving(true);
    setError(null);
    try {
      const c = await qrApi.createCampaign({ name: name.trim(), description: description.trim() || undefined });
      router.replace(`/qr-campaigns/${c.campaign_id}`);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to create campaign');
      setSaving(false);
    }
  }

  return (
    <div className="mx-auto max-w-lg space-y-6">
      <div>
        <h1 className="text-2xl font-bold text-gray-900">Create QR Campaign</h1>
        <p className="text-sm text-gray-500">Add placements and download QR codes after creating.</p>
      </div>
      <Card className="p-6">
        {error && <ErrorBox message={error} />}
        <form onSubmit={submit} className="space-y-4">
          <div>
            <label className="label">Campaign name</label>
            <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. Backlit box" required />
          </div>
          <div>
            <label className="label">Description (optional)</label>
            <textarea className="input" value={description} onChange={(e) => setDescription(e.target.value)} rows={3} />
          </div>
          <button type="submit" className="btn-primary w-full" disabled={saving}>
            {saving ? 'Creating…' : 'Create campaign'}
          </button>
        </form>
      </Card>
    </div>
  );
}
```

- [ ] **Step 4: Detail page** `admin-dashboard/src/app/qr-campaigns/[id]/page.tsx` (placements + QR cards, add-placement form, enable/disable toggles, analytics with date range, bulk ZIP download):

```tsx
'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { useParams } from 'next/navigation';
import JSZip from 'jszip';
import { Plus, Download } from 'lucide-react';
import { qrApi } from '@/lib/qrApi';
import type { QrCampaignDetail, QrPlacement, QrAnalytics } from '@/lib/qrTypes';
import { Card, Loading, ErrorBox, Badge, Stat, useToast } from '@/components/ui';
import { QrCard } from '@/components/qr/QrCard';
import { ScanBars } from '@/components/qr/ScanBars';
import { svgElementToPngBlob, downloadBlob, sanitizeFilename } from '@/lib/qrImage';

export default function QrCampaignDetailPage() {
  const params = useParams<{ id: string }>();
  const id = params.id;
  const toast = useToast();

  const [detail, setDetail] = useState<QrCampaignDetail | null>(null);
  const [analytics, setAnalytics] = useState<QrAnalytics | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [pName, setPName] = useState('');
  const [pLoc, setPLoc] = useState('');
  const [adding, setAdding] = useState(false);
  const gridRef = useRef<HTMLDivElement>(null);

  const today = new Date();
  const monthAgo = new Date(today.getTime() - 30 * 24 * 3600 * 1000);
  const [from, setFrom] = useState(monthAgo.toISOString().slice(0, 10));
  const [to, setTo] = useState(today.toISOString().slice(0, 10));

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [d, a] = await Promise.all([
        qrApi.getCampaign(id),
        qrApi.analytics(id, `${from}T00:00:00Z`, `${to}T23:59:59Z`)
      ]);
      setDetail(d);
      setAnalytics(a);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load campaign');
    } finally {
      setLoading(false);
    }
  }, [id, from, to]);

  useEffect(() => {
    void load();
  }, [load]);

  async function addPlacement(e: React.FormEvent) {
    e.preventDefault();
    if (!pName.trim()) return;
    setAdding(true);
    try {
      await qrApi.addPlacement(id, { name: pName.trim(), location: pLoc.trim() || undefined });
      setPName('');
      setPLoc('');
      toast.push('success', 'Placement added');
      await load();
    } catch (e) {
      toast.push('error', e instanceof Error ? e.message : 'Failed to add placement');
    } finally {
      setAdding(false);
    }
  }

  async function toggleCampaign(enabled: boolean) {
    try {
      await qrApi.updateCampaign(id, { enabled });
      toast.push('success', enabled ? 'Campaign enabled' : 'Campaign disabled');
      await load();
    } catch (e) {
      toast.push('error', e instanceof Error ? e.message : 'Failed to update');
    }
  }

  async function togglePlacement(p: QrPlacement) {
    try {
      await qrApi.updatePlacement(id, p.slug, { enabled: !p.enabled });
      await load();
    } catch (e) {
      toast.push('error', e instanceof Error ? e.message : 'Failed to update placement');
    }
  }

  async function downloadAllZip() {
    if (!detail || !gridRef.current) return;
    const cards = Array.from(gridRef.current.querySelectorAll('svg')) as SVGSVGElement[];
    if (!cards.length) return;
    const zip = new JSZip();
    for (let i = 0; i < detail.placements.length; i++) {
      const svg = cards[i];
      if (!svg) continue;
      const blob = await svgElementToPngBlob(svg, 1024);
      const p = detail.placements[i];
      zip.file(`${sanitizeFilename(p.name)}_${p.slug}.png`, blob);
    }
    const out = await zip.generateAsync({ type: 'blob' });
    downloadBlob(out, `${sanitizeFilename(detail.campaign.name)}_qr_codes.zip`);
  }

  if (loading) return <Loading label="Loading campaign…" />;
  if (error) return <ErrorBox message={error} />;
  if (!detail) return <ErrorBox message="Campaign not found" />;

  const c = detail.campaign;

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-2xl font-bold text-gray-900">{c.name}</h1>
          {c.description && <p className="text-sm text-gray-500">{c.description}</p>}
        </div>
        <div className="flex items-center gap-2 print:hidden">
          <Badge tone={c.enabled ? 'green' : 'gray'}>{c.enabled ? 'Active' : 'Disabled'}</Badge>
          <button className="btn-ghost text-sm" onClick={() => toggleCampaign(!c.enabled)}>
            {c.enabled ? 'Disable' : 'Enable'}
          </button>
        </div>
      </div>

      {analytics && (
        <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
          <Stat label="Total scans" value={analytics.total_scans} />
          <Stat label="Unique" value={analytics.total_unique} />
          <Stat label="iOS" value={analytics.total_ios} />
          <Stat label="Android" value={analytics.total_android} />
        </div>
      )}

      <Card className="p-4">
        <div className="mb-3 flex flex-wrap items-end justify-between gap-3">
          <h2 className="text-lg font-semibold text-gray-900">Scans over time</h2>
          <div className="flex items-end gap-2 print:hidden">
            <div>
              <label className="label">From</label>
              <input type="date" className="input" value={from} onChange={(e) => setFrom(e.target.value)} />
            </div>
            <div>
              <label className="label">To</label>
              <input type="date" className="input" value={to} onChange={(e) => setTo(e.target.value)} />
            </div>
          </div>
        </div>
        {analytics ? <ScanBars data={analytics.daily} /> : <div className="text-sm text-gray-400">No data</div>}
      </Card>

      <Card className="p-4">
        <h2 className="mb-3 text-lg font-semibold text-gray-900">Add placement</h2>
        <form onSubmit={addPlacement} className="flex flex-wrap items-end gap-3">
          <div className="flex-1">
            <label className="label">Name</label>
            <input className="input" value={pName} onChange={(e) => setPName(e.target.value)} placeholder="e.g. Manda Hill entrance" required />
          </div>
          <div className="flex-1">
            <label className="label">Location (optional)</label>
            <input className="input" value={pLoc} onChange={(e) => setPLoc(e.target.value)} placeholder="e.g. Lusaka" />
          </div>
          <button type="submit" className="btn-primary flex items-center gap-1.5" disabled={adding}>
            <Plus className="h-4 w-4" /> {adding ? 'Adding…' : 'Add'}
          </button>
        </form>
      </Card>

      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold text-gray-900">Placements ({detail.placements.length})</h2>
        {detail.placements.length > 0 && (
          <button className="btn-ghost flex items-center gap-1.5 text-sm print:hidden" onClick={downloadAllZip}>
            <Download className="h-4 w-4" /> Download all (ZIP)
          </button>
        )}
      </div>

      <div ref={gridRef} className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
        {detail.placements.map((p) => (
          <div key={p.slug} className="space-y-2">
            <QrCard placement={p} />
            <div className="flex items-center justify-between px-1 text-xs print:hidden">
              <span className="text-gray-500">{p.scan_count} scans · {p.unique_count} unique</span>
              <button className="text-brand-green hover:underline" onClick={() => togglePlacement(p)}>
                {p.enabled ? 'Disable' : 'Enable'}
              </button>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
```

- [ ] **Step 5: Typecheck + lint + build.** `cd admin-dashboard && npm run typecheck && npm run lint && npm run build` → Expected: builds clean.

- [ ] **Step 6: Commit.**

```bash
git -C admin-dashboard add src/app/qr-campaigns src/components/qr/ScanBars.tsx
git -C admin-dashboard commit -m "feat(qr): add QR campaign list/create/detail pages with analytics"
```

---

## Self-Review Notes (spec coverage)

- Campaigns→placements two-level model: Tasks 1–2 (model), 3 (service), 4 (handlers), 8 (UI). ✅
- Slug-keyed redirect, no GSI: Task 1 (`QRPLACEMENT!{slug}`), Task 2 (`GetPlacement`), Task 4 (`Redirect`). ✅
- OS detection → App Store/Play Store/web: Task 3 (`ClassifyUserAgent`/`ResolveDestination`). ✅
- Scans-only tracking; timestamp, platform, UA, bot flag, unique cookie; no geo: Tasks 2–4. ✅
- Raw events + atomic counters + TTL: Task 2 (`RecordScan`, `scanEventTTL`). ✅
- Bots redirected + flagged + excluded: Task 3 (`ClassifyUserAgent`), Task 2 (`RecordScan` bot branch). ✅
- Client-side plain QR, PNG + print + bulk ZIP: Tasks 7–8. ✅
- Lifecycle (add placements, enable/disable, edit): Tasks 2–4, 8. ✅
- Analytics in dashboard: total + per-placement table + scans-over-time, date-filterable: Tasks 3, 8. ✅
- One-shot delivery: single plan. ✅
