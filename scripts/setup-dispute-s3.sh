#!/usr/bin/env bash
# Create the private S3 bucket for dispute (and print) uploads, attach IAM to qcom EC2,
# and optionally store S3_BUCKET in SSM.
#
# The global name "printdrop-documents" is owned by another AWS account, so production
# uses a per-account bucket name by default.
#
# Usage:
#   AWS_REGION=ap-southeast-2 ./scripts/setup-dispute-s3.sh
#   BUCKET=my-bucket-name ./scripts/setup-dispute-s3.sh
#
set -euo pipefail

REGION="${AWS_REGION:-ap-southeast-2}"
ACCOUNT_ID="$(aws sts get-caller-identity --query Account --output text)"
BUCKET="${S3_BUCKET:-bunzo-qcom-documents-${ACCOUNT_ID}}"
ROLE="${QCOM_IAM_ROLE:-qcom-ec2-role}"
SSM_PREFIX="${SSM_PREFIX:-/qcom/prod}"
TABLE="${DYNAMODB_TABLE_NAME:-QComTable}"
SEED_DISPUTE_CONFIG="${SEED_DISPUTE_CONFIG:-true}"

echo "=== Dispute / upload S3 setup ==="
echo "Region:  ${REGION}"
echo "Account: ${ACCOUNT_ID}"
echo "Bucket:  ${BUCKET}"
echo "Role:    ${ROLE}"

bucket_exists() {
  aws s3api head-bucket --bucket "$1" --region "$REGION" >/dev/null 2>&1
}

if bucket_exists "$BUCKET"; then
  echo "Bucket s3://${BUCKET} already exists"
else
  echo "Creating bucket s3://${BUCKET}..."
  if [[ "$REGION" == "us-east-1" ]]; then
    aws s3api create-bucket --bucket "$BUCKET" --region "$REGION"
  else
    aws s3api create-bucket \
      --bucket "$BUCKET" \
      --region "$REGION" \
      --create-bucket-configuration "LocationConstraint=${REGION}"
  fi
fi

echo "Blocking public access..."
aws s3api put-public-access-block \
  --bucket "$BUCKET" \
  --region "$REGION" \
  --public-access-block-configuration \
    BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true

echo "Enabling default SSE-S3 encryption..."
aws s3api put-bucket-encryption \
  --bucket "$BUCKET" \
  --region "$REGION" \
  --server-side-encryption-configuration \
    '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"},"BucketKeyEnabled":true}]}'

echo "Setting CORS (presigned PUT from app / web)..."
aws s3api put-bucket-cors \
  --bucket "$BUCKET" \
  --region "$REGION" \
  --cors-configuration '{
    "CORSRules": [{
      "AllowedHeaders": ["*"],
      "AllowedMethods": ["PUT", "GET", "HEAD"],
      "AllowedOrigins": ["*"],
      "ExposeHeaders": ["ETag"],
      "MaxAgeSeconds": 3600
    }]
  }'

echo "Attaching S3 policy to ${ROLE}..."
POLICY_DOC=$(cat <<EOF
{
  "Version": "2012-10-17",
  "Statement": [{
    "Sid": "QcomUploadPresign",
    "Effect": "Allow",
    "Action": [
      "s3:PutObject",
      "s3:GetObject",
      "s3:DeleteObject",
      "s3:ListBucket",
      "s3:GetBucketLocation"
    ],
    "Resource": [
      "arn:aws:s3:::${BUCKET}",
      "arn:aws:s3:::${BUCKET}/disputes/*",
      "arn:aws:s3:::${BUCKET}/printdrop/*"
    ]
  }]
}
EOF
)
aws iam put-role-policy \
  --role-name "$ROLE" \
  --policy-name qcom-s3-uploads \
  --policy-document "$POLICY_DOC"

echo "Writing SSM ${SSM_PREFIX}/S3_BUCKET=${BUCKET}..."
aws ssm put-parameter \
  --name "${SSM_PREFIX}/S3_BUCKET" \
  --type String \
  --value "$BUCKET" \
  --overwrite \
  --region "$REGION" \
  --no-cli-pager >/dev/null

if [[ "$SEED_DISPUTE_CONFIG" == "true" ]]; then
  echo "Updating DynamoDB upload use-case registry..."
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
  TABLE="$TABLE" S3_BUCKET="$BUCKET" AWS_REGION="$REGION" \
    "${SCRIPT_DIR}/seed-dispute-config.sh"
fi

echo ""
echo "=== Done ==="
echo "Bucket: s3://${BUCKET}"
echo "Prefixes: disputes/ (dispute photos), printdrop/ (print files)"
echo ""
echo "Note: qcom EC2 picks up new IAM within ~15 min, or restart qcom:"
echo "  sudo systemctl restart qcom"
echo "Or redeploy: make deploy"
