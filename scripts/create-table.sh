#!/bin/bash

# Create DynamoDB table for QCom with all production GSIs.
# DynamoDB allows only one GSI update at a time — each create waits for ACTIVE.

TABLE_NAME="${DYNAMODB_TABLE_NAME:-QComTable}"
ENDPOINT="${DYNAMODB_ENDPOINT:-}"
REGION="${DYNAMODB_REGION:-us-east-1}"

endpoint_args=()
if [[ -n "$ENDPOINT" ]]; then
  endpoint_args=(--endpoint-url "$ENDPOINT")
fi

wait_for_table_active() {
  echo "Waiting for table $TABLE_NAME to become ACTIVE..."
  aws dynamodb wait table-exists \
    --table-name "$TABLE_NAME" \
    "${endpoint_args[@]}" \
    --region "$REGION"
  for _ in $(seq 1 60); do
    local table_status
    table_status=$(aws dynamodb describe-table \
      --table-name "$TABLE_NAME" \
      "${endpoint_args[@]}" \
      --region "$REGION" \
      --query 'Table.TableStatus' --output text)
    if [[ "$table_status" == "ACTIVE" ]]; then
      return 0
    fi
    sleep 5
  done
  echo "Timed out waiting for table ACTIVE" >&2
  return 1
}

wait_for_gsi_active() {
  local index_name="$1"
  echo "Waiting for GSI $index_name to become ACTIVE..."
  for _ in $(seq 1 120); do
    local gsi_status
    gsi_status=$(aws dynamodb describe-table \
      --table-name "$TABLE_NAME" \
      "${endpoint_args[@]}" \
      --region "$REGION" \
      --query "Table.GlobalSecondaryIndexes[?IndexName=='${index_name}'].IndexStatus | [0]" \
      --output text)
    if [[ "$gsi_status" == "ACTIVE" ]]; then
      echo "GSI $index_name is ACTIVE"
      return 0
    fi
    sleep 10
  done
  echo "Timed out waiting for GSI $index_name" >&2
  return 1
}

gsi_exists() {
  local index_name="$1"
  local count
  count=$(aws dynamodb describe-table \
    --table-name "$TABLE_NAME" \
    "${endpoint_args[@]}" \
    --region "$REGION" \
    --query "length(Table.GlobalSecondaryIndexes[?IndexName=='${index_name}'])" \
    --output text 2>/dev/null || echo 0)
  [[ "$count" != "0" ]]
}

# Usage: create_gsi <name> <gsi_json> <attr_def> [<attr_def> ...]
create_gsi() {
  local index_name="$1"
  local gsi_json="$2"
  shift 2
  local attr_defs=("$@")

  if gsi_exists "$index_name"; then
    echo "GSI $index_name already exists"
    wait_for_gsi_active "$index_name"
    return 0
  fi

  echo "Adding $index_name GSI..."

  aws dynamodb update-table \
    --table-name "$TABLE_NAME" \
    --attribute-definitions "${attr_defs[@]}" \
    --global-secondary-index-updates "$gsi_json" \
    "${endpoint_args[@]}" \
    --region "$REGION" \
    --no-cli-pager

  wait_for_gsi_active "$index_name"
}

echo "Creating DynamoDB table: $TABLE_NAME (region=$REGION)"

if aws dynamodb describe-table \
  --table-name "$TABLE_NAME" \
  "${endpoint_args[@]}" \
  --region "$REGION" >/dev/null 2>&1; then
  echo "Table $TABLE_NAME already exists"
else
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
fi

wait_for_table_active

# UserIdIndex — address queries
create_gsi "UserIdIndex" \
  '[{"Create":{"IndexName":"UserIdIndex","KeySchema":[{"AttributeName":"user_id","KeyType":"HASH"},{"AttributeName":"created_at","KeyType":"RANGE"}],"Projection":{"ProjectionType":"ALL"}}}]' \
  "AttributeName=user_id,AttributeType=S" \
  "AttributeName=created_at,AttributeType=S"

# OrderIndex — trip lookup by order_id
create_gsi "OrderIndex" \
  '[{"Create":{"IndexName":"OrderIndex","KeySchema":[{"AttributeName":"order_id","KeyType":"HASH"}],"Projection":{"ProjectionType":"ALL"}}}]' \
  "AttributeName=order_id,AttributeType=S"

# DisputeOrderIndex — dispute lookup by order number (sparse: only dispute items set dispute_order_number)
create_gsi "DisputeOrderIndex" \
  '[{"Create":{"IndexName":"DisputeOrderIndex","KeySchema":[{"AttributeName":"dispute_order_number","KeyType":"HASH"},{"AttributeName":"created_at","KeyType":"RANGE"}],"Projection":{"ProjectionType":"ALL"}}}]' \
  "AttributeName=dispute_order_number,AttributeType=S" \
  "AttributeName=created_at,AttributeType=S"

# DETripsIndex — DE earnings / payout queries
create_gsi "DETripsIndex" \
  '[{"Create":{"IndexName":"DETripsIndex","KeySchema":[{"AttributeName":"de_id","KeyType":"HASH"},{"AttributeName":"completed_at","KeyType":"RANGE"}],"Projection":{"ProjectionType":"ALL"}}}]' \
  "AttributeName=de_id,AttributeType=S" \
  "AttributeName=completed_at,AttributeType=S"

# DEDutyIndex — assignment cron (matches production: duty_index_key + updated_at)
create_gsi "DEDutyIndex" \
  '[{"Create":{"IndexName":"DEDutyIndex","KeySchema":[{"AttributeName":"duty_index_key","KeyType":"HASH"},{"AttributeName":"updated_at","KeyType":"RANGE"}],"Projection":{"ProjectionType":"ALL"}}}]' \
  "AttributeName=duty_index_key,AttributeType=S" \
  "AttributeName=updated_at,AttributeType=S"

# ReferralCodeIndex — O(1) referrer lookup
create_gsi "ReferralCodeIndex" \
  '[{"Create":{"IndexName":"ReferralCodeIndex","KeySchema":[{"AttributeName":"referral_code","KeyType":"HASH"}],"Projection":{"ProjectionType":"ALL"}}}]' \
  "AttributeName=referral_code,AttributeType=S"

echo "All GSIs provisioned."

# Enable TTL on the table
echo "Enabling TTL on table..."
aws dynamodb update-time-to-live \
  --table-name "$TABLE_NAME" \
  --time-to-live-specification "Enabled=true,AttributeName=TTL" \
  "${endpoint_args[@]}" \
  --region "$REGION" \
  --no-cli-pager 2>/dev/null || echo "TTL may already be enabled, continuing..."

echo "TTL enabled successfully!"
