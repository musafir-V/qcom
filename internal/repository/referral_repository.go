package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type ReferralRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewReferralRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *ReferralRepository {
	return &ReferralRepository{client: client, tableName: tableName, logger: logger}
}

func (r *ReferralRepository) Create(ctx context.Context, ref *models.Referral) error {
	op := logging.Start(ctx, r.logger, "ReferralRepository.Create", logrus.Fields{
		"referrer": ref.ReferrerDEID, "referred": ref.ReferredDEID,
	})
	defer op.End()

	now := time.Now().UTC().Format(time.RFC3339)
	ref.CreatedAt = now
	ref.Status = models.ReferralStatusActive

	item, err := attributevalue.MarshalMap(ref)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal referral: %w", err))
	}
	item["PK"] = &types.AttributeValueMemberS{Value: ref.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: ref.GetSK()}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(r.tableName),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return op.Outcome("already_exists", fmt.Errorf("referred DE already has a referral"))
		}
		return op.Fail(fmt.Errorf("failed to create referral: %w", err))
	}
	return nil
}

func (r *ReferralRepository) GetByReferredDEID(ctx context.Context, referredDEID string) (*models.Referral, error) {
	op := logging.Start(ctx, r.logger, "ReferralRepository.GetByReferredDEID", logrus.Fields{"referred_de_id": referredDEID})
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "REFERRAL!" + referredDEID},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get referral: %w", err))
	}
	if result.Item == nil {
		return nil, nil
	}

	var ref models.Referral
	if err := attributevalue.UnmarshalMap(result.Item, &ref); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal referral: %w", err))
	}
	return &ref, nil
}

func (r *ReferralRepository) MarkCompleted(ctx context.Context, referredDEID, triggeredAt string) error {
	op := logging.Start(ctx, r.logger, "ReferralRepository.MarkCompleted", logrus.Fields{"referred_de_id": referredDEID})
	defer op.End()

	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "REFERRAL!" + referredDEID},
			"SK": &types.AttributeValueMemberS{Value: "METADATA"},
		},
		UpdateExpression: aws.String("SET #status = :completed, payout_triggered_at = :triggered"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":completed": &types.AttributeValueMemberS{Value: string(models.ReferralStatusCompleted)},
			":triggered": &types.AttributeValueMemberS{Value: triggeredAt},
			":active":    &types.AttributeValueMemberS{Value: string(models.ReferralStatusActive)},
		},
		ConditionExpression: aws.String("#status = :active"),
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return op.Outcome("already_completed", fmt.Errorf("referral already completed or expired"))
		}
		return op.Fail(fmt.Errorf("failed to mark referral completed: %w", err))
	}
	return nil
}

// ListByReferrerDEID scans for referrals where referrer_de_id matches.
// Uses a filter scan — add a GSI on referrer_de_id if referrer list becomes large.
func (r *ReferralRepository) ListByReferrerDEID(ctx context.Context, referrerDEID string) ([]*models.Referral, error) {
	op := logging.Start(ctx, r.logger, "ReferralRepository.ListByReferrerDEID", logrus.Fields{"referrer_de_id": referrerDEID})
	defer op.End()

	result, err := r.client.Scan(ctx, &dynamodb.ScanInput{
		TableName:        aws.String(r.tableName),
		FilterExpression: aws.String("referrer_de_id = :rid AND begins_with(PK, :prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":rid":    &types.AttributeValueMemberS{Value: referrerDEID},
			":prefix": &types.AttributeValueMemberS{Value: "REFERRAL!"},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to list referrals by referrer: %w", err))
	}

	var refs []*models.Referral
	for _, item := range result.Items {
		var ref models.Referral
		if err := attributevalue.UnmarshalMap(item, &ref); err != nil {
			continue
		}
		refs = append(refs, &ref)
	}
	op.With("count", len(refs))
	return refs, nil
}
