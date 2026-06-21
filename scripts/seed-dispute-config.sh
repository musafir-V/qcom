#!/usr/bin/env bash
# Seeds the upload use-case registry and dispute dispositions into DynamoDB.
# Usage: TABLE=QComTable AWS_REGION=ap-southeast-2 ./scripts/seed-dispute-config.sh
set -euo pipefail

TABLE="${TABLE:-QComTable}"
BUCKET="${S3_BUCKET:-printdrop-documents}"

put() {
  aws dynamodb put-item --table-name "$TABLE" --item "$1"
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

# --- Dispute dispositions ---
disposition() {
  # args: code title photos_required photo_min description_required display_order
  put '{
    "PK": {"S": "CONFIG"}, "SK": {"S": "DISPUTE_DISPOSITION!'"$1"'"},
    "code": {"S": "'"$1"'"}, "title": {"S": "'"$2"'"},
    "photos_required": {"BOOL": '"$3"'}, "photo_min": {"N": "'"$4"'"},
    "description_required": {"BOOL": '"$5"'}, "display_order": {"N": "'"$6"'"},
    "active": {"BOOL": true}
  }'
}

disposition "ITEM_MISSING"   "An item was missing"      false 0 false 1
disposition "ITEM_DAMAGED"   "An item was damaged"      true  1 false 2
disposition "WRONG_ITEM"     "I received the wrong item" true  1 false 3
disposition "QUALITY_ISSUE"  "Quality was not good"     false 0 true  4
disposition "NEVER_ARRIVED"  "Order never arrived"      false 0 true  5
disposition "OTHER"          "Something else"           false 0 true  6

echo "Seeded dispute config into $TABLE."
