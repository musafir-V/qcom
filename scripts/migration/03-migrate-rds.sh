#!/usr/bin/env bash
# Cross-account RDS migration via snapshot share + restore. Keeps instance name quickcommerce.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OLD_ENV="${ROOT}/.deploy.local.env"
NEW_ENV="${ROOT}/.deploy.new.env"
REGION="${AWS_REGION:-ap-southeast-2}"

[[ -f "$OLD_ENV" && -f "$NEW_ENV" ]] || { echo "Missing env files" >&2; exit 1; }

set -a; source "$NEW_ENV"; set +a
unset AWS_PROFILE AWS_DEFAULT_PROFILE AWS_SESSION_TOKEN

DB_ID="${RDS_INSTANCE_ID:-quickcommerce}"
NEW_ACCOUNT="${NEW_ACCOUNT_ID:?Set NEW_ACCOUNT_ID}"
DB_SUBNET_GROUP="${DB_SUBNET_GROUP:-bunzo-db-subnet-group}"
SNAP_ID="migration-$(date +%Y%m%d%H%M)"

echo "=== 2C: RDS ${DB_ID} ==="

if aws rds describe-db-instances --db-instance-identifier "$DB_ID" --region "$REGION" >/dev/null 2>&1; then
  ENDPOINT=$(aws rds describe-db-instances --db-instance-identifier "$DB_ID" --region "$REGION" \
    --query 'DBInstances[0].Endpoint.Address' --output text)
  echo "RDS ${DB_ID} already exists in new account: ${ENDPOINT}"
  exit 0
fi

echo "Creating snapshot in old account..."
(
  set -a; source "$OLD_ENV"; set +a
  unset AWS_PROFILE AWS_DEFAULT_PROFILE AWS_SESSION_TOKEN
  aws rds create-db-snapshot \
    --db-instance-identifier "$DB_ID" \
    --db-snapshot-identifier "$SNAP_ID" \
    --region "$REGION"
  aws rds wait db-snapshot-available --db-snapshot-identifier "$SNAP_ID" --region "$REGION"
  SNAP_ARN=$(aws rds describe-db-snapshots --db-snapshot-identifier "$SNAP_ID" --region "$REGION" \
    --query 'DBSnapshots[0].DBSnapshotArn' --output text)
  echo "Snapshot: ${SNAP_ARN}"
  aws rds modify-db-snapshot-attribute \
    --db-snapshot-identifier "$SNAP_ID" \
    --attribute-name restore \
    --values-to-add "$NEW_ACCOUNT" \
    --region "$REGION"
  echo "$SNAP_ARN" > "${ROOT}/data/rds-snapshot-arn.txt"
)

SNAP_ARN=$(cat "${ROOT}/data/rds-snapshot-arn.txt")
OLD_ACCOUNT="${OLD_ACCOUNT_ID:-119312949433}"
COPY_ID="${SNAP_ID}-copy"

CMK_SNAP="${SNAP_ID}-cmk"
COPY_ID="${SNAP_ID}-copy"
OLD_CMK_FILE="${ROOT}/data/rds-migration-cmk-arn.txt"

echo "Re-encrypting snapshot with customer CMK in old account (required for cross-account)..."
(
  set -a; source "$OLD_ENV"; set +a
  unset AWS_PROFILE AWS_DEFAULT_PROFILE AWS_SESSION_TOKEN
  if ! aws rds describe-db-snapshots --db-snapshot-identifier "$CMK_SNAP" --region "$REGION" >/dev/null 2>&1; then
    CMK_ARN=$(aws kms create-key --description "RDS cross-account migration" --region "$REGION" \
      --query 'KeyMetadata.Arn' --output text)
    echo "$CMK_ARN" > "$OLD_CMK_FILE"
    cat > /tmp/kms-cross-account.json <<POLICY
{
  "Version": "2012-10-17",
  "Statement": [
    {"Sid":"Root","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::${OLD_ACCOUNT}:root"},"Action":"kms:*","Resource":"*"},
    {"Sid":"AllowNewAccountRDS","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::${NEW_ACCOUNT}:root"},
     "Action":["kms:Decrypt","kms:DescribeKey","kms:CreateGrant"],"Resource":"*",
     "Condition":{"StringEquals":{"kms:ViaService":"rds.${REGION}.amazonaws.com"}}}
  ]
}
POLICY
    aws kms put-key-policy --key-id "$CMK_ARN" --policy-name default --policy file:///tmp/kms-cross-account.json --region "$REGION"
    aws rds copy-db-snapshot --source-db-snapshot-identifier "$SNAP_ID" \
      --target-db-snapshot-identifier "$CMK_SNAP" --kms-key-id "$CMK_ARN" --region "$REGION"
    aws rds wait db-snapshot-available --db-snapshot-identifier "$CMK_SNAP" --region "$REGION"
    aws rds modify-db-snapshot-attribute --db-snapshot-identifier "$CMK_SNAP" \
      --attribute-name restore --values-to-add "$NEW_ACCOUNT" --region "$REGION"
  fi
)
SOURCE_SNAP_ARN="arn:aws:rds:${REGION}:${OLD_ACCOUNT}:snapshot:${CMK_SNAP}"

echo "Copying snapshot into new account..."
if ! aws rds describe-db-snapshots --db-snapshot-identifier "$COPY_ID" --region "$REGION" >/dev/null 2>&1; then
  NEW_CMK=$(aws kms create-key --description "RDS quickcommerce encryption" --region "$REGION" \
    --query 'KeyMetadata.Arn' --output text)
  aws rds copy-db-snapshot \
    --source-db-snapshot-identifier "$SOURCE_SNAP_ARN" \
    --target-db-snapshot-identifier "$COPY_ID" \
    --kms-key-id "$NEW_CMK" \
    --region "$REGION"
fi
aws rds wait db-snapshot-available --db-snapshot-identifier "$COPY_ID" --region "$REGION"

echo "Restoring as ${DB_ID}..."
aws rds restore-db-instance-from-db-snapshot \
  --db-instance-identifier "$DB_ID" \
  --db-snapshot-identifier "$COPY_ID" \
  --db-subnet-group-name "$DB_SUBNET_GROUP" \
  --publicly-accessible \
  --region "$REGION"

echo "Waiting for RDS (5-15 min)..."
aws rds wait db-instance-available --db-instance-identifier "$DB_ID" --region "$REGION"

ENDPOINT=$(aws rds describe-db-instances --db-instance-identifier "$DB_ID" --region "$REGION" \
  --query 'DBInstances[0].Endpoint.Address' --output text)
echo "RDS migration OK: ${DB_ID} → ${ENDPOINT}"
echo "${ENDPOINT}" > "${ROOT}/data/rds-new-endpoint.txt"
