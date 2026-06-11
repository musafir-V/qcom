#!/usr/bin/env bash
# Cross-account SSM migration: copy /qcom/prod/* verbatim (backward compatible).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OLD_ENV="${ROOT}/.deploy.local.env"
NEW_ENV="${ROOT}/.deploy.new.env"
REGION="${AWS_REGION:-ap-southeast-2}"
PREFIX="/qcom/prod"
EXPORT="${ROOT}/data/ssm-export-merged.json"

[[ -f "$OLD_ENV" && -f "$NEW_ENV" ]] || { echo "Missing env files" >&2; exit 1; }

echo "=== 2D: SSM ${PREFIX}/* ==="

(
  set -a; source "$OLD_ENV"; set +a
  unset AWS_PROFILE AWS_DEFAULT_PROFILE AWS_SESSION_TOKEN
  aws ssm get-parameters-by-path \
    --path "$PREFIX" --recursive --with-decryption \
    --region "$REGION" --output json > "$EXPORT"
  OLD_COUNT=$(jq '.Parameters | length' "$EXPORT")
  echo "Exported ${OLD_COUNT} parameters"
)

(
  set -a; source "$NEW_ENV"; set +a
  unset AWS_PROFILE AWS_DEFAULT_PROFILE AWS_SESSION_TOKEN
  jq -c '.Parameters[]' "$EXPORT" | while read -r param; do
    NAME=$(echo "$param" | jq -r '.Name')
    VALUE=$(echo "$param" | jq -r '.Value')
    TYPE=$(echo "$param" | jq -r '.Type // "SecureString"')
    echo "  Importing ${NAME}..."
    aws ssm put-parameter --name "$NAME" --value "$VALUE" --type "$TYPE" \
      --overwrite --region "$REGION" >/dev/null
  done
  NEW_COUNT=$(aws ssm get-parameters-by-path --path "$PREFIX" --recursive --region "$REGION" \
    --query 'Parameters[*].Name' --output text | tr '\t' '\n' | grep -c . || echo 0)
  OLD_COUNT=$(jq '.Parameters | length' "$EXPORT")
  echo "Old: ${OLD_COUNT}  New: ${NEW_COUNT}"
  [[ "$OLD_COUNT" == "$NEW_COUNT" ]] || { echo "SSM COUNT MISMATCH" >&2; exit 1; }
)

echo "SSM migration OK"
