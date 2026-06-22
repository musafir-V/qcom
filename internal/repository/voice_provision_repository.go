package repository

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/qcom/qcom/internal/logging"
	"github.com/sirupsen/logrus"
)

const voiceProvisionSK = "PROVISIONED"

// voiceProvisionPK returns the DynamoDB PK for a given sub.
func voiceProvisionPK(sub string) string {
	return "VOICEUSER!" + sub
}

// VoiceProvisionRepository persists lazy Vonage user-provisioning flags.
// Each provisioned sub is stored as a single row: PK="VOICEUSER!<sub>", SK="PROVISIONED".
type VoiceProvisionRepository struct {
	client    *dynamodb.Client
	tableName string
	logger    *logrus.Logger
}

// NewVoiceProvisionRepository constructs a VoiceProvisionRepository.
func NewVoiceProvisionRepository(client *dynamodb.Client, tableName string, logger *logrus.Logger) *VoiceProvisionRepository {
	return &VoiceProvisionRepository{client: client, tableName: tableName, logger: logger}
}

// IsProvisioned returns true if a Vonage user has already been created for sub.
func (r *VoiceProvisionRepository) IsProvisioned(ctx context.Context, sub string) (bool, error) {
	op := logging.Start(ctx, r.logger, "VoiceProvisionRepository.IsProvisioned", logrus.Fields{"sub": sub})
	defer op.End()

	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: voiceProvisionPK(sub)},
			"SK": &types.AttributeValueMemberS{Value: voiceProvisionSK},
		},
	})
	if err != nil {
		return false, op.Fail(fmt.Errorf("voice provision IsProvisioned: %w", err))
	}
	return result.Item != nil, nil
}

// MarkProvisioned records that a Vonage user has been created for sub.
func (r *VoiceProvisionRepository) MarkProvisioned(ctx context.Context, sub string) error {
	op := logging.Start(ctx, r.logger, "VoiceProvisionRepository.MarkProvisioned", logrus.Fields{"sub": sub})
	defer op.End()

	_, err := r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName),
		Item: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: voiceProvisionPK(sub)},
			"SK": &types.AttributeValueMemberS{Value: voiceProvisionSK},
		},
	})
	if err != nil {
		return op.Fail(fmt.Errorf("voice provision MarkProvisioned: %w", err))
	}
	return nil
}
