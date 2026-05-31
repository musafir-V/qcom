#!/bin/bash

# Create DynamoDB table for QCom
# Run this script after starting DynamoDB local

TABLE_NAME="${DYNAMODB_TABLE_NAME:-QComTable}"
ENDPOINT="${DYNAMODB_ENDPOINT:-}"
REGION="${DYNAMODB_REGION:-us-east-1}"

endpoint_args=()
if [[ -n "$ENDPOINT" ]]; then
  endpoint_args=(--endpoint-url "$ENDPOINT")
fi

echo "Creating DynamoDB table: $TABLE_NAME (region=$REGION)"

aws dynamodb create-table \
  --table-name "$TABLE_NAME" \
  --attribute-definitions \
    AttributeName=PK,AttributeType=S \
    AttributeName=SK,AttributeType=S \
  --key-schema \
    AttributeName=PK,KeyType=HASH \
    AttributeName=SK,KeyType=RANGE \
  --billing-mode PAY_PER_REQUEST \
  "${endpoint_args[@]}" \
  --region "$REGION" \
  --no-cli-pager

echo "Table created successfully!"

# Add UserIdIndex GSI for address queries
echo "Adding UserIdIndex GSI..."
aws dynamodb update-table \
  --table-name "$TABLE_NAME" \
  --attribute-definitions \
    AttributeName=user_id,AttributeType=S \
    AttributeName=created_at,AttributeType=S \
  --global-secondary-index-updates '[
    {
      "Create": {
        "IndexName": "UserIdIndex",
        "KeySchema": [
          {"AttributeName": "user_id", "KeyType": "HASH"},
          {"AttributeName": "created_at", "KeyType": "RANGE"}
        ],
        "Projection": {"ProjectionType": "ALL"}
      }
    }
  ]' \
  "${endpoint_args[@]}" \
  --region "$REGION" \
  --no-cli-pager 2>/dev/null || echo "GSI may already exist, continuing..."

echo "GSI setup complete!"

# Add DEDutyIndex GSI for querying eligible DEs by store (used by assignment cron)
echo "Adding DEDutyIndex GSI..."
aws dynamodb update-table \
  --table-name "$TABLE_NAME" \
  --attribute-definitions \
    AttributeName=duty_index_key,AttributeType=S \
  --global-secondary-index-updates '[
    {
      "Create": {
        "IndexName": "DEDutyIndex",
        "KeySchema": [
          {"AttributeName": "duty_index_key", "KeyType": "HASH"}
        ],
        "Projection": {"ProjectionType": "ALL"}
      }
    }
  ]' \
  "${endpoint_args[@]}" \
  --region "$REGION" \
  --no-cli-pager 2>/dev/null || echo "DEDutyIndex GSI may already exist, continuing..."

echo "DEDutyIndex GSI setup complete!"

# Enable TTL on the table
echo "Enabling TTL on table..."
aws dynamodb update-time-to-live \
  --table-name "$TABLE_NAME" \
  --time-to-live-specification "Enabled=true,AttributeName=TTL" \
  "${endpoint_args[@]}" \
  --region "$REGION" \
  --no-cli-pager

echo "TTL enabled successfully!"

