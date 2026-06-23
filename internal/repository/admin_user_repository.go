package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/logging"
	"github.com/qcom/qcom/internal/models"
	"github.com/sirupsen/logrus"
)

// ErrAdminUserExists is returned by Create when an account with the same
// username already exists.
var ErrAdminUserExists = errors.New("admin user already exists")

type AdminUserRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewAdminUserRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *AdminUserRepository {
	return &AdminUserRepository{client: client, tableName: tableName, logger: logger}
}

func (r *AdminUserRepository) GetByUsername(ctx context.Context, username string) (*models.AdminUser, error) {
	op := logging.Start(ctx, r.logger, "AdminUser.GetByUsername", logrus.Fields{"username": username})
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "ADMIN_USER"},
			"SK": &types.AttributeValueMemberS{Value: username},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to get admin user: %w", err))
	}
	if result.Item == nil {
		op.With("found", false)
		return nil, nil
	}

	var user models.AdminUser
	if err := attributevalue.UnmarshalMap(result.Item, &user); err != nil {
		return nil, op.Fail(fmt.Errorf("failed to unmarshal admin user: %w", err))
	}
	return &user, nil
}

// Create writes a new admin user, failing with ErrAdminUserExists if one with
// the same username is already present.
func (r *AdminUserRepository) Create(ctx context.Context, user *models.AdminUser) error {
	op := logging.Start(ctx, r.logger, "AdminUser.Create", logrus.Fields{"username": user.Username})
	defer op.End()

	item, err := marshalAdminUserItem(user)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal admin user: %w", err))
	}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(r.tableName),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return op.Outcome("already_exists", ErrAdminUserExists)
		}
		return op.Fail(fmt.Errorf("failed to create admin user: %w", err))
	}
	return nil
}

// Put overwrites an admin user (used for updates such as password changes).
func (r *AdminUserRepository) Put(ctx context.Context, user *models.AdminUser) error {
	op := logging.Start(ctx, r.logger, "AdminUser.Put", logrus.Fields{"username": user.Username})
	defer op.End()

	item, err := marshalAdminUserItem(user)
	if err != nil {
		return op.Fail(fmt.Errorf("failed to marshal admin user: %w", err))
	}

	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item:      item,
	})
	if err != nil {
		return op.Fail(fmt.Errorf("failed to put admin user: %w", err))
	}
	return nil
}

func (r *AdminUserRepository) ListAll(ctx context.Context) ([]*models.AdminUser, error) {
	op := logging.Start(ctx, r.logger, "AdminUser.ListAll", logrus.Fields{})
	defer op.End()

	var out []*models.AdminUser
	var lastKey map[string]types.AttributeValue
	for {
		result, err := r.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(r.tableName),
			KeyConditionExpression: aws.String("PK = :pk"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk": &types.AttributeValueMemberS{Value: "ADMIN_USER"},
			},
			ExclusiveStartKey: lastKey,
		})
		if err != nil {
			return nil, op.Fail(fmt.Errorf("failed to list admin users: %w", err))
		}

		for _, item := range result.Items {
			var user models.AdminUser
			if err := attributevalue.UnmarshalMap(item, &user); err != nil {
				op.Logger().WithError(err).Warn("failed to unmarshal admin user; skipping")
				continue
			}
			out = append(out, &user)
		}

		if len(result.LastEvaluatedKey) == 0 {
			break
		}
		lastKey = result.LastEvaluatedKey
	}

	op.With("count", len(out))
	return out, nil
}

func marshalAdminUserItem(user *models.AdminUser) (map[string]types.AttributeValue, error) {
	item, err := attributevalue.MarshalMap(user)
	if err != nil {
		return nil, err
	}
	item["PK"] = &types.AttributeValueMemberS{Value: user.GetPK()}
	item["SK"] = &types.AttributeValueMemberS{Value: user.GetSK()}
	return item, nil
}
