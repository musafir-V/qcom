#!/usr/bin/env bash
# Phase 1: New account foundation setup.
# Usage: source .deploy.new.env && ./scripts/migration/01-foundation.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
ENV_FILE="${REPO_ROOT}/.deploy.new.env"

if [[ -f "${ENV_FILE}" ]]; then
  set -a
  # shellcheck source=/dev/null
  source "${ENV_FILE}"
  set +a
fi

unset AWS_PROFILE AWS_DEFAULT_PROFILE AWS_SESSION_TOKEN

REGION="${AWS_REGION:-ap-southeast-2}"
DB_SUBNET_GROUP="${DB_SUBNET_GROUP:-bunzo-db-subnet-group}"
BUDGET_NAME="${BUDGET_NAME:-bunzo-monthly-budget}"
BUDGET_LIMIT="${BUDGET_LIMIT:-50}"
OUT="${REPO_ROOT}/data/migration-foundation.json"

mkdir -p "${REPO_ROOT}/data"

echo "=== Phase 1: Foundation (${REGION}) ==="

ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
echo "Account: ${ACCOUNT_ID}"

VPC_ID=$(aws ec2 describe-vpcs --filters Name=isDefault,Values=true \
  --query 'Vpcs[0].VpcId' --output text --region "${REGION}")
if [[ -z "${VPC_ID}" || "${VPC_ID}" == "None" ]]; then
  echo "ERROR: no default VPC in ${REGION}" >&2
  exit 1
fi
echo "VPC: ${VPC_ID}"

SUBNET_IDS=()
while IFS= read -r sid; do
  [[ -n "${sid}" ]] && SUBNET_IDS+=("${sid}")
done < <(aws ec2 describe-subnets \
  --filters Name=vpc-id,Values="${VPC_ID}" \
  --query 'Subnets[*].SubnetId' --output text --region "${REGION}" | tr '\t' '\n')
if [[ ${#SUBNET_IDS[@]} -lt 2 ]]; then
  echo "ERROR: need at least 2 subnets, found ${#SUBNET_IDS[@]}" >&2
  exit 1
fi
echo "Subnets (${#SUBNET_IDS[@]}): ${SUBNET_IDS[*]}"

if aws rds describe-db-subnet-groups --db-subnet-group-name "${DB_SUBNET_GROUP}" \
  --region "${REGION}" >/dev/null 2>&1; then
  echo "DB subnet group ${DB_SUBNET_GROUP} already exists"
else
  aws rds create-db-subnet-group \
    --db-subnet-group-name "${DB_SUBNET_GROUP}" \
    --db-subnet-group-description "Bunzo RDS subnet group" \
    --subnet-ids "${SUBNET_IDS[@]}" \
    --region "${REGION}"
  echo "Created DB subnet group: ${DB_SUBNET_GROUP}"
fi

echo "S3: skipped (printdrop-documents not migrated)"

if aws budgets describe-budgets --account-id "${ACCOUNT_ID}" \
  --query "Budgets[?BudgetName=='${BUDGET_NAME}'].BudgetName" --output text 2>/dev/null | grep -q .; then
  echo "Budget ${BUDGET_NAME} already exists"
else
  START=$(date -u +%Y-%m-01)
  END=$(date -u -v+1y +%Y-%m-01 2>/dev/null || date -u -d '+1 year' +%Y-%m-01)
  aws budgets create-budget \
    --account-id "${ACCOUNT_ID}" \
    --budget "{
      \"BudgetName\": \"${BUDGET_NAME}\",
      \"BudgetLimit\": {\"Amount\": \"${BUDGET_LIMIT}\", \"Unit\": \"USD\"},
      \"TimeUnit\": \"MONTHLY\",
      \"BudgetType\": \"COST\",
      \"TimePeriod\": {\"Start\": \"${START}\", \"End\": \"${END}\"}
    }" 2>/dev/null && echo "Created budget: ${BUDGET_NAME} (\$${BUDGET_LIMIT}/month)" \
    || echo "WARN: budget creation skipped (may need console setup or extra IAM permissions)"
fi

cat > "${OUT}" <<EOF
{
  "phase": 1,
  "completed_at": "$(date -u +"%Y-%m-%dT%H:%M:%SZ")",
  "account_id": "${ACCOUNT_ID}",
  "region": "${REGION}",
  "vpc_id": "${VPC_ID}",
  "subnet_ids": $(printf '%s\n' "${SUBNET_IDS[@]}" | jq -R . | jq -s .),
  "db_subnet_group": "${DB_SUBNET_GROUP}",
  "s3_bucket": null
}
EOF

echo ""
echo "=== Phase 1 complete ==="
echo "Foundation manifest: ${OUT}"
cat "${OUT}"
