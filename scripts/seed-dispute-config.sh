#!/usr/bin/env bash
# Seeds the upload use-case registry and dispute dispositions into DynamoDB.
# Usage: TABLE=QComTable AWS_REGION=ap-southeast-2 ./scripts/seed-dispute-config.sh
set -euo pipefail

TABLE="${TABLE:-QComTable}"
ACCOUNT_ID="$(aws sts get-caller-identity --query Account --output text 2>/dev/null || echo "")"
DEFAULT_BUCKET="bunzo-qcom-documents-${ACCOUNT_ID}"
BUCKET="${S3_BUCKET:-${DEFAULT_BUCKET}}"
REGION="${AWS_REGION:-ap-southeast-2}"

put() {
  aws dynamodb put-item --table-name "$TABLE" --region "$REGION" --item "$1"
}

deactivate_disposition() {
  local code="$1"
  aws dynamodb update-item \
    --table-name "$TABLE" \
    --region "$REGION" \
    --key "{\"PK\":{\"S\":\"CONFIG\"},\"SK\":{\"S\":\"DISPUTE_DISPOSITION!${code}\"}}" \
    --update-expression "SET active = :f" \
    --expression-attribute-values '{":f":{"BOOL":false}}' \
    --no-cli-pager 2>/dev/null || true
}

# --- Upload use-cases ---
put '{
  "PK": {"S": "CONFIG"}, "SK": {"S": "UPLOAD_USECASE!dispute_photo"},
  "use_case": {"S": "dispute_photo"}, "bucket": {"S": "'"$BUCKET"'"},
  "key_prefix": {"S": "disputes"},
  "allowed_mime_types": {"L": [{"S": "image/jpeg"}, {"S": "image/png"}, {"S": "image/heic"}]},
  "max_file_size": {"N": "10485760"},
  "allowed_entity_types": {"L": [{"S": "customer"}]}
}'

put '{
  "PK": {"S": "CONFIG"}, "SK": {"S": "UPLOAD_USECASE!print_file"},
  "use_case": {"S": "print_file"}, "bucket": {"S": "'"$BUCKET"'"},
  "key_prefix": {"S": "printdrop"},
  "allowed_mime_types": {"L": [{"S": "application/pdf"}, {"S": "application/msword"}, {"S": "application/vnd.openxmlformats-officedocument.wordprocessingml.document"}, {"S": "image/jpeg"}, {"S": "image/png"}, {"S": "image/heic"}]},
  "max_file_size": {"N": "52428800"},
  "allowed_entity_types": {"L": [{"S": "customer"}]}
}'

# --- Dispute dispositions (Help & Support screen) ---
# args: code title subtitle photos_required photo_min description_required display_order
disposition() {
  local code="$1" title="$2" subtitle="$3"
  local photos_required="$4" photo_min="$5" description_required="$6" display_order="$7"
  python3 - "$TABLE" "$REGION" "$code" "$title" "$subtitle" \
    "$photos_required" "$photo_min" "$description_required" "$display_order" <<'PY'
import json, subprocess, sys

table, region, code, title, subtitle = sys.argv[1:6]
photos_required, photo_min, description_required, display_order = sys.argv[6:]

item = {
    "PK": {"S": "CONFIG"},
    "SK": {"S": f"DISPUTE_DISPOSITION!{code}"},
    "code": {"S": code},
    "title": {"S": title},
    "subtitle": {"S": subtitle},
    "photos_required": {"BOOL": photos_required.lower() == "true"},
    "photo_min": {"N": photo_min},
    "description_required": {"BOOL": description_required.lower() == "true"},
    "display_order": {"N": display_order},
    "active": {"BOOL": True},
}
subprocess.run(
    ["aws", "dynamodb", "put-item", "--table-name", table, "--region", region, "--item", json.dumps(item)],
    check=True,
)
print(f"  + {code}")
PY
}

echo "Seeding dispute dispositions..."

disposition "ORDER_NOT_RECEIVED" \
  "I did not receive this order" \
  "Your order was marked delivered but you did not receive it, or the delivery could not be completed." \
  false 0 true 1

disposition "ITEMS_DIFFERENT" \
  "Item(s) are different from what I ordered" \
  "You received a different product, size, flavour, or variant than what you ordered." \
  true 1 false 2

disposition "DAMAGED_ITEMS" \
  "I have received damaged items" \
  "One or more items arrived broken, crushed, torn, or otherwise damaged." \
  true 1 false 3

disposition "PACKAGING_ISSUES" \
  "I have packaging issues with my order" \
  "The outer package or item packaging was torn, open, or not sealed properly." \
  true 1 false 4

disposition "EXPIRED_ITEMS" \
  "I have received expired items" \
  "One or more items were past their expiry or best-before date when delivered." \
  true 1 false 5

disposition "ITEMS_MISSING" \
  "Items are missing from my order" \
  "Some items from your order were not included in the delivery bag or box." \
  false 0 true 6

disposition "BAD_QUALITY" \
  "I have received bad quality items" \
  "Items arrived spoiled, stale, or not fit to use or consume." \
  true 1 false 7

disposition "RETURN_ITEMS" \
  "I want to return items in my order" \
  "You would like to return one or more items from this order." \
  true 1 true 8

disposition "PAYMENT_REFUND" \
  "I have payment and refund related issues" \
  "You were charged incorrectly, did not receive a refund, or have another payment concern." \
  false 0 true 9

echo "Deactivating legacy placeholder dispositions..."
for legacy in ITEM_MISSING ITEM_DAMAGED WRONG_ITEM QUALITY_ISSUE NEVER_ARRIVED OTHER; do
  deactivate_disposition "$legacy"
done

echo "Seeded dispute config into $TABLE."
