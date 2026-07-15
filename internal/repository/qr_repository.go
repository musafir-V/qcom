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
