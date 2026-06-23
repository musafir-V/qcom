#!/bin/bash
# Migrate OrderIndex from order_id → trip_order_id (sparse: trip items only).
# VOICECTX rows keep order_id for webhook resolution but no longer pollute the index.
#
# Run before deploying code that queries trip_order_id on OrderIndex.
# Usage: source .deploy.local.env && ./scripts/migrate-trip-order-index.sh

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
      "${endpoint_args[@]+"${endpoint_args[@]}"}" \
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
  echo "Waiting for OrderIndex to be removed..."
  for _ in $(seq 1 120); do
    local count
    count=$(aws dynamodb describe-table \
      --table-name "$TABLE_NAME" \
      "${endpoint_args[@]+"${endpoint_args[@]}"}" \
      --region "$REGION" \
      --query "length(Table.GlobalSecondaryIndexes[?IndexName=='OrderIndex'])" \
      --output text 2>/dev/null || echo 0)
    if [[ "$count" == "0" ]]; then
      echo "OrderIndex removed"
      return 0
    fi
    sleep 10
  done
  echo "Timed out waiting for GSI deletion" >&2
  return 1
}

wait_for_gsi_active() {
  echo "Waiting for OrderIndex to become ACTIVE..."
  for _ in $(seq 1 120); do
    local gsi_status
    gsi_status=$(aws dynamodb describe-table \
      --table-name "$TABLE_NAME" \
      "${endpoint_args[@]+"${endpoint_args[@]}"}" \
      --region "$REGION" \
      --query "Table.GlobalSecondaryIndexes[?IndexName=='OrderIndex'].IndexStatus | [0]" \
      --output text)
    if [[ "$gsi_status" == "ACTIVE" ]]; then
      echo "OrderIndex is ACTIVE (trip_order_id)"
      return 0
    fi
    echo "  index status: $gsi_status"
    sleep 10
  done
  echo "Timed out waiting for GSI ACTIVE" >&2
  return 1
}

backfill_trip_order_id() {
  echo "Backfilling trip_order_id on TRIP! items..."
  python3 - "$TABLE_NAME" "$REGION" "$ENDPOINT" <<'PY'
import json, subprocess, sys

table, region, endpoint = sys.argv[1], sys.argv[2], sys.argv[3]
endpoint_args = ["--endpoint-url", endpoint] if endpoint else []

def aws_json(*args):
    cmd = ["aws", "dynamodb", *args, "--region", region, "--output", "json", *endpoint_args]
    try:
        out = subprocess.check_output(cmd, text=True, stderr=subprocess.STDOUT)
    except subprocess.CalledProcessError as e:
        print("AWS command failed:", " ".join(cmd), file=sys.stderr)
        print(e.output, file=sys.stderr)
        raise
    if not out.strip():
        return {}
    return json.loads(out)

def aws_update(*args):
    cmd = ["aws", "dynamodb", *args, "--region", region, *endpoint_args]
    subprocess.check_call(cmd, stdout=subprocess.DEVNULL)

updated = 0
scanned = 0
start_key = None
while True:
    scan_args = [
        "scan", "--table-name", table,
        "--filter-expression", "begins_with(PK, :pk) AND SK = :sk",
        "--expression-attribute-values", json.dumps({
            ":pk": {"S": "TRIP!"},
            ":sk": {"S": "METADATA"},
        }),
    ]
    if start_key:
        scan_args.extend(["--exclusive-start-key", json.dumps(start_key)])
    page = aws_json(*scan_args)
    for item in page.get("Items", []):
        scanned += 1
        order_id = item.get("order_id", {}).get("S", "")
        if not order_id:
            continue
        if item.get("trip_order_id", {}).get("S") == order_id:
            continue
        pk = item["PK"]["S"]
        sk = item["SK"]["S"]
        aws_update(
            "update-item", "--table-name", table,
            "--key", json.dumps({"PK": {"S": pk}, "SK": {"S": sk}}),
            "--update-expression", "SET trip_order_id = :oid",
            "--expression-attribute-values", json.dumps({":oid": {"S": order_id}}),
        )
        updated += 1
        print(f"  backfilled {pk} -> {order_id}")
    start_key = page.get("LastEvaluatedKey")
    if not start_key:
        break

print(f"Backfill done: scanned={scanned} updated={updated}")
PY
}

echo "Migrating OrderIndex on $TABLE_NAME (region=$REGION)"

current_key=$(aws dynamodb describe-table \
  --table-name "$TABLE_NAME" \
  "${endpoint_args[@]+"${endpoint_args[@]}"}" \
  --region "$REGION" \
  --query "Table.GlobalSecondaryIndexes[?IndexName=='OrderIndex'] | [0].KeySchema[?KeyType=='HASH'].AttributeName | [0]" \
  --output text 2>/dev/null || echo "None")

if [[ "$current_key" == "trip_order_id" ]]; then
  echo "OrderIndex already uses trip_order_id — running backfill only"
  backfill_trip_order_id
  exit 0
fi

backfill_trip_order_id

if [[ "$current_key" == "order_id" ]]; then
  echo "Deleting legacy OrderIndex (order_id)..."
  aws dynamodb update-table \
    --table-name "$TABLE_NAME" \
    --global-secondary-index-updates '[{"Delete":{"IndexName":"OrderIndex"}}]' \
    "${endpoint_args[@]+"${endpoint_args[@]}"}" \
    --region "$REGION" \
    --no-cli-pager
  wait_for_gsi_gone
  wait_for_table_active
elif [[ "$current_key" != "None" && -n "$current_key" ]]; then
  echo "Unexpected OrderIndex hash key: $current_key" >&2
  exit 1
else
  echo "OrderIndex does not exist — will create"
fi

echo "Creating OrderIndex (trip_order_id, sparse)..."
aws dynamodb update-table \
  --table-name "$TABLE_NAME" \
  --attribute-definitions AttributeName=trip_order_id,AttributeType=S \
  --global-secondary-index-updates \
    '[{"Create":{"IndexName":"OrderIndex","KeySchema":[{"AttributeName":"trip_order_id","KeyType":"HASH"}],"Projection":{"ProjectionType":"ALL"}}}]' \
  "${endpoint_args[@]+"${endpoint_args[@]}"}" \
  --region "$REGION" \
  --no-cli-pager

wait_for_gsi_active
echo "Migration complete. Deploy qcom code that queries trip_order_id on OrderIndex."
