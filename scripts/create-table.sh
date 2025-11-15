#!/bin/bash

# Create DynamoDB table for QCom
# Run this script after starting DynamoDB local

TABLE_NAME="${DYNAMODB_TABLE_NAME:-QComTable}"
ENDPOINT="${DYNAMODB_ENDPOINT:-http://localhost:8000}"
REGION="${DYNAMODB_REGION:-us-east-1}"

echo "Creating DynamoDB table: $TABLE_NAME"

aws dynamodb create-table \
  --table-name "$TABLE_NAME" \
  --attribute-definitions \
    AttributeName=PK,AttributeType=S \
    AttributeName=SK,AttributeType=S \
  --key-schema \
    AttributeName=PK,KeyType=HASH \
    AttributeName=SK,KeyType=RANGE \
  --billing-mode PAY_PER_REQUEST \
  --endpoint-url "$ENDPOINT" \
  --region "$REGION" \
  --no-cli-pager

echo "Table created successfully!"

