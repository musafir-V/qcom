#!/usr/bin/env bash
# Migrate QComTable from one AWS region to another via scan + batch-write.
#
# Usage:
#   ./scripts/migrate-dynamodb-region.sh \
#     --source-region ap-southeast-1 \
#     --target-region ap-southeast-2 \
#     [--export-file data/dynamodb-export-ap-southeast-1.json]
#
# Creates the target table (same schema as create-table.sh), imports all items,
# and enables TTL. Safe to re-run: skips table creation if it already exists.

set -euo pipefail

TABLE_NAME="${DYNAMODB_TABLE_NAME:-QComTable}"
SOURCE_REGION=""
TARGET_REGION=""
EXPORT_FILE=""

usage() {
  echo "Usage: $0 --source-region REGION --target-region REGION [--export-file PATH]"
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --source-region) SOURCE_REGION="$2"; shift 2 ;;
    --target-region) TARGET_REGION="$2"; shift 2 ;;
    --export-file)   EXPORT_FILE="$2"; shift 2 ;;
    *) usage ;;
  esac
done

[[ -n "$SOURCE_REGION" && -n "$TARGET_REGION" ]] || usage

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EXPORT_FILE="${EXPORT_FILE:-$ROOT/data/dynamodb-export-${SOURCE_REGION}.json}"

echo "=== DynamoDB region migration ==="
echo "Table:  $TABLE_NAME"
echo "Source: $SOURCE_REGION"
echo "Target: $TARGET_REGION"
echo "Export: $EXPORT_FILE"
echo ""

if [[ ! -f "$EXPORT_FILE" ]]; then
  echo "Export file not found. Scanning source table..."
  python3 - "$EXPORT_FILE" "$SOURCE_REGION" "$TABLE_NAME" <<'PY'
import json, subprocess, sys
path, region, table = sys.argv[1:4]
items, start_key = [], None
while True:
    cmd = ["aws", "dynamodb", "scan", "--region", region, "--table-name", table, "--output", "json"]
    if start_key:
        cmd += ["--exclusive-start-key", json.dumps(start_key)]
    data = json.loads(subprocess.check_output(cmd, text=True))
    items.extend(data.get("Items", []))
    start_key = data.get("LastEvaluatedKey")
    print(f"  scanned {len(items)} items...", flush=True)
    if not start_key:
        break
with open(path, "w") as f:
    json.dump({"TableName": table, "SourceRegion": region, "ItemCount": len(items), "Items": items}, f)
print(f"Wrote {len(items)} items to {path}")
PY
fi

if aws dynamodb describe-table --table-name "$TABLE_NAME" --region "$TARGET_REGION" >/dev/null 2>&1; then
  echo "Target table already exists in $TARGET_REGION — skipping creation."
else
  echo "Creating target table in $TARGET_REGION..."
  DYNAMODB_REGION="$TARGET_REGION" DYNAMODB_ENDPOINT="" DYNAMODB_TABLE_NAME="$TABLE_NAME" \
    "$ROOT/scripts/create-table.sh"
  echo "Waiting for table to become ACTIVE..."
  aws dynamodb wait table-exists --table-name "$TABLE_NAME" --region "$TARGET_REGION"
  until [[ "$(aws dynamodb describe-table --table-name "$TABLE_NAME" --region "$TARGET_REGION" --query 'Table.TableStatus' --output text)" == "ACTIVE" ]]; do
    sleep 5
  done
  echo "Table ACTIVE. Adding GSIs and TTL..."
  DYNAMODB_REGION="$TARGET_REGION" DYNAMODB_ENDPOINT="" DYNAMODB_TABLE_NAME="$TABLE_NAME" \
    "$ROOT/scripts/create-table.sh" || true
  sleep 15
fi

echo "Importing items into $TARGET_REGION..."
python3 - "$EXPORT_FILE" "$TARGET_REGION" "$TABLE_NAME" <<'PY'
import json, subprocess, sys, time

path, region, table = sys.argv[1:4]
data = json.load(open(path))
items = data["Items"]
print(f"Importing {len(items)} items...")

def batch_write(batch):
    payload = json.dumps({"RequestItems": {table: [
        {"PutRequest": {"Item": item}} for item in batch
    ]}})
    subprocess.check_call([
        "aws", "dynamodb", "batch-write-item",
        "--region", region,
        "--cli-input-json", payload,
    ], stdout=subprocess.DEVNULL)

batch_size = 25
for i in range(0, len(items), batch_size):
    batch = items[i:i + batch_size]
    for attempt in range(5):
        try:
            batch_write(batch)
            break
        except subprocess.CalledProcessError:
            if attempt == 4:
                raise
            time.sleep(2 ** attempt)
    print(f"  wrote {min(i + batch_size, len(items))}/{len(items)}")

count = json.loads(subprocess.check_output([
    "aws", "dynamodb", "scan", "--region", region, "--table-name", table,
    "--select", "COUNT", "--output", "json",
], text=True))["Count"]
print(f"Target table item count: {count}")
PY

echo ""
echo "Migration complete."
echo "Next: update SSM /qcom/prod/DYNAMODB_REGION to $TARGET_REGION and restart qcom on EC2."
