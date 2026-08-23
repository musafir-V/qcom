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

type CashDepositLedgerRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

func NewCashDepositLedgerRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *CashDepositLedgerRepository {
	return &CashDepositLedgerRepository{client: client, tableName: tableName, logger: logger}
}

// ListByDE returns all cash-deposit entries for a DE. deID must be the rider
// phone (CashDepositLedger.DEID), not the prefixed DE id.
func (r *CashDepositLedgerRepository) ListByDE(ctx context.Context, deID string) ([]*models.CashDepositLedger, error) {
	op := logging.Start(ctx, r.logger, "CashDepositLedger.ListByDE", logrus.Fields{"de_id": deID})
	defer op.End()

	result, err := r.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(r.tableName),
		KeyConditionExpression: aws.String("PK = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: "CASHDEP!" + deID},
		},
	})
	if err != nil {
		return nil, op.Fail(fmt.Errorf("failed to list cash deposits: %w", err))
	}

	var items []*models.CashDepositLedger
	for _, item := range result.Items {
		var e models.CashDepositLedger
		if err := attributevalue.UnmarshalMap(item, &e); err != nil {
			continue
		}
		items = append(items, &e)
	}

	op.With("count", len(items))
	return items, nil
}
