#!/bin/bash
# Replace DisputeOrderIndex hash key: dispute_order_id → dispute_order_number.
# DynamoDB cannot change GSI key schema in place — delete then recreate.
# Safe when the index is empty or disputes will be re-indexed on next write.

set -euo pipefail

TABLE_NAME="${DYNAMODB_TABLE_NAME:-QComTable}"
REGION="${DYNAMODB_REGION:-ap-southeast-2}"
ENDPOINT="${DYNAMODB_ENDPOINT:-}"

endpoint_args=()
if [[ -n "$ENDPOINT" ]]; then
  endpoint_args=(--endpoint-url "$ENDPOINT")
fi

wait_for_table_active() {
  echo "Waiting for table $TABLE_NAME to become ACTIVE..."
  for _ in $(seq 1 120); do
    local status
    status=$(aws dynamodb describe-table \
      --table-name "$TABLE_NAME" \
      "${endpoint_args[@]}" \
      --region "$REGION" \
      --query 'Table.TableStatus' --output text)
    if [[ "$status" == "ACTIVE" ]]; then
      return 0
    fi
    echo "  table status: $status"
    sleep 10
  done
  echo "Timed out waiting for table ACTIVE" >&2
  return 1
}

wait_for_gsi_gone() {
  echo "Waiting for DisputeOrderIndex to be removed..."
  for _ in $(seq 1 120); do
    local count
    count=$(aws dynamodb describe-table \
      --table-name "$TABLE_NAME" \
      "${endpoint_args[@]}" \
      --region "$REGION" \
      --query "length(Table.GlobalSecondaryIndexes[?IndexName=='DisputeOrderIndex'])" \
      --output text 2>/dev/null || echo 0)
    if [[ "$count" == "0" ]]; then
      echo "DisputeOrderIndex removed"
      return 0
    fi
    sleep 10
  done
  echo "Timed out waiting for GSI deletion" >&2
  return 1
}

wait_for_gsi_active() {
  echo "Waiting for DisputeOrderIndex to become ACTIVE..."
  for _ in $(seq 1 120); do
    local gsi_status
    gsi_status=$(aws dynamodb describe-table \
      --table-name "$TABLE_NAME" \
      "${endpoint_args[@]}" \
      --region "$REGION" \
      --query "Table.GlobalSecondaryIndexes[?IndexName=='DisputeOrderIndex'].IndexStatus | [0]" \
      --output text)
    if [[ "$gsi_status" == "ACTIVE" ]]; then
      echo "DisputeOrderIndex is ACTIVE (dispute_order_number + created_at)"
      return 0
    fi
    echo "  index status: $gsi_status"
    sleep 10
  done
  echo "Timed out waiting for GSI ACTIVE" >&2
  return 1
}

echo "Migrating DisputeOrderIndex on $TABLE_NAME (region=$REGION)"

current_key=$(aws dynamodb describe-table \
  --table-name "$TABLE_NAME" \
  "${endpoint_args[@]}" \
  --region "$REGION" \
  --query "Table.GlobalSecondaryIndexes[?IndexName=='DisputeOrderIndex'].KeySchema[?KeyType=='HASH'].AttributeName | [0]" \
  --output text 2>/dev/null || echo "None")

if [[ "$current_key" == "dispute_order_number" ]]; then
  echo "DisputeOrderIndex already uses dispute_order_number — nothing to do"
  exit 0
fi

if [[ "$current_key" == "dispute_order_id" ]]; then
  echo "Deleting legacy DisputeOrderIndex (dispute_order_id)..."
  aws dynamodb update-table \
    --table-name "$TABLE_NAME" \
    --global-secondary-index-updates '[{"Delete":{"IndexName":"DisputeOrderIndex"}}]' \
    "${endpoint_args[@]}" \
    --region "$REGION" \
    --no-cli-pager
  wait_for_gsi_gone
  wait_for_table_active
elif [[ "$current_key" != "None" && -n "$current_key" ]]; then
  echo "Unexpected DisputeOrderIndex hash key: $current_key" >&2
  exit 1
else
  echo "DisputeOrderIndex does not exist — will create"
fi

echo "Creating DisputeOrderIndex (dispute_order_number + created_at)..."
aws dynamodb update-table \
  --table-name "$TABLE_NAME" \
  --attribute-definitions \
    AttributeName=dispute_order_number,AttributeType=S \
    AttributeName=created_at,AttributeType=S \
  --global-secondary-index-updates \
    '[{"Create":{"IndexName":"DisputeOrderIndex","KeySchema":[{"AttributeName":"dispute_order_number","KeyType":"HASH"},{"AttributeName":"created_at","KeyType":"RANGE"}],"Projection":{"ProjectionType":"ALL"}}}]' \
  "${endpoint_args[@]}" \
  --region "$REGION" \
  --no-cli-pager

wait_for_gsi_active
echo "Migration complete."
