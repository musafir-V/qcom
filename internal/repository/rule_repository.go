package repository

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

type RuleRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewRuleRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *RuleRepository {
	return &RuleRepository{client: client, tableName: tableName, logger: logger}
}

func (r *RuleRepository) Put(ctx context.Context, rule *models.Rule) error {
	op := logging.Start(ctx, r.logger, "Rule.Put", logrus.Fields{
		"rule_id": rule.ID, "family": string(rule.Family), "version": rule.Version,
	})
	defer op.End()

	item, err := marshalRuleItem(rule)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal rule: %w", err))
	}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to write rule: %w", err))
	}
	return nil
}

func (r *RuleRepository) ListAll(ctx context.Context) ([]*models.Rule, error) {
	op := logging.Start(ctx, r.logger, "Rule.ListAll", logrus.Fields{})
	defer op.End()

	var out []*models.Rule
	var lastKey map[string]types.AttributeValue
	for {
		result, err := r.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(r.tableName),
			KeyConditionExpression: aws.String("PK = :pk"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk": &types.AttributeValueMemberS{Value: "RULE"},
			},
			ExclusiveStartKey: lastKey,
		})
		if err != nil {
			return nil, op.Fail(fmt.Errorf("failed to list rules: %w", err))
		}

		for _, item := range result.Items {
			var rule models.Rule
			if err := attributevalue.UnmarshalMap(item, &rule); err != nil {
				op.Logger().WithError(err).Warn("failed to unmarshal rule; skipping")
				continue
			}
			out = append(out, &rule)
		}

		if len(result.LastEvaluatedKey) == 0 {
			break
		}
		lastKey = result.LastEvaluatedKey
	}

	op.With("count", len(out))
	return out, nil
}

func (r *RuleRepository) GetLatest(ctx context.Context, family models.RuleFamily, id string) (*models.Rule, error) {
	op := logging.Start(ctx, r.logger, "Rule.GetLatest", logrus.Fields{
		"rule_id": id, "family": string(family),
	})
	defer op.End()

	prefix := latestRulePrefix(family, id)
	var latest *models.Rule
	var lastKey map[string]types.AttributeValue
	for {
		result, err := r.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(r.tableName),
			KeyConditionExpression: aws.String("PK = :pk AND begins_with(SK, :prefix)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk":     &types.AttributeValueMemberS{Value: "RULE"},
				":prefix": &types.AttributeValueMemberS{Value: prefix},
			},
			ExclusiveStartKey: lastKey,
		})
		if err != nil {
			return nil, op.Fail(fmt.Errorf("failed to query latest rule: %w", err))
		}

		for _, item := range result.Items {
			var current models.Rule
			if err := attributevalue.UnmarshalMap(item, &current); err != nil {
				op.Logger().WithError(err).Warn("failed to unmarshal rule; skipping")
				continue
			}
			if latest == nil || current.Version > latest.Version {
				cp := current
				latest = &cp
			}
		}

		if len(result.LastEvaluatedKey) == 0 {
			break
		}
		lastKey = result.LastEvaluatedKey
	}
	return latest, nil
}

func latestRulePrefix(family models.RuleFamily, id string) string {
	return fmt.Sprintf("%s#%s#v", family, id)
}

func marshalRuleItem(rule *models.Rule) (map[string]types.AttributeValue, error) {
	item, err := attributevalue.MarshalMap(rule)
	if err != nil {
		return nil, err
	}
	item["PK"] = &types.AttributeValueMemberS{Value: rule.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: rule.GetSK()}
	return item, nil
}
