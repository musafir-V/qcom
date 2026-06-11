#!/usr/bin/env bash
# Cross-account DynamoDB migration: old account → new account, same table name.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OLD_ENV="${ROOT}/.deploy.local.env"
NEW_ENV="${ROOT}/.deploy.new.env"
TABLE="${DYNAMODB_TABLE_NAME:-QComTable}"
REGION="${AWS_REGION:-ap-southeast-2}"
EXPORT="${ROOT}/data/dynamodb-export-old-account.json"

[[ -f "$OLD_ENV" && -f "$NEW_ENV" ]] || { echo "Missing .deploy.local.env or .deploy.new.env" >&2; exit 1; }

echo "=== 2A: DynamoDB ${TABLE} ==="

echo "Exporting from old account..."
(
  set -a; source "$OLD_ENV"; set +a
  unset AWS_PROFILE AWS_DEFAULT_PROFILE AWS_SESSION_TOKEN
  python3 - "$EXPORT" "$REGION" "$TABLE" <<'PY'
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
    json.dump({"TableName": table, "ItemCount": len(items), "Items": items}, f)
print(f"Exported {len(items)} items")
PY
)

OLD_COUNT=$(python3 -c "import json; print(json.load(open('$EXPORT'))['ItemCount'])")
echo "Old account items: ${OLD_COUNT}"

echo "Creating table + importing in new account..."
(
  set -a; source "$NEW_ENV"; set +a
  unset AWS_PROFILE AWS_DEFAULT_PROFILE AWS_SESSION_TOKEN
  if aws dynamodb describe-table --table-name "$TABLE" --region "$REGION" >/dev/null 2>&1; then
    echo "Table ${TABLE} already exists in new account"
  else
    DYNAMODB_REGION="$REGION" DYNAMODB_TABLE_NAME="$TABLE" "$ROOT/scripts/create-table.sh"
    aws dynamodb wait table-exists --table-name "$TABLE" --region "$REGION"
    sleep 10
  fi
  python3 - "$EXPORT" "$REGION" "$TABLE" <<'PY'
import json, subprocess, sys, time
path, region, table = sys.argv[1:4]
items = json.load(open(path))["Items"]
print(f"Importing {len(items)} items...")
for i in range(0, len(items), 25):
    batch = items[i:i+25]
    payload = json.dumps({"RequestItems": {table: [{"PutRequest": {"Item": it}} for it in batch]}})
    for attempt in range(5):
        try:
            subprocess.check_call(["aws","dynamodb","batch-write-item","--region",region,"--cli-input-json",payload], stdout=subprocess.DEVNULL)
            break
        except subprocess.CalledProcessError:
            if attempt == 4: raise
            time.sleep(2**attempt)
    print(f"  wrote {min(i+25,len(items))}/{len(items)}")
PY
  NEW_COUNT=$(aws dynamodb scan --table-name "$TABLE" --select COUNT --region "$REGION" --query Count --output text)
  echo "New account items: ${NEW_COUNT}"
  [[ "$OLD_COUNT" == "$NEW_COUNT" ]] || { echo "COUNT MISMATCH old=${OLD_COUNT} new=${NEW_COUNT}" >&2; exit 1; }
)

echo "DynamoDB migration OK: ${OLD_COUNT} items"
