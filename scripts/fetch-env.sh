#!/usr/bin/env bash
# Fetches all qcom production secrets from SSM Parameter Store and writes /app/.env.
# Requires the EC2 IAM Instance Profile to have ssm:GetParametersByPath on /qcom/prod/*.

set -euo pipefail

SSM_PREFIX="/qcom/prod"
ENV_FILE="/app/.env"
REGION=$(curl -s http://169.254.169.254/latest/meta-data/placement/region)

echo "Fetching config from SSM prefix ${SSM_PREFIX} in region ${REGION}..."

aws ssm get-parameters-by-path \
  --path "${SSM_PREFIX}" \
  --with-decryption \
  --region "${REGION}" \
  --query "Parameters[*].[Name,Value]" \
  --output text | while IFS=$'\t' read -r name value; do
    key="${name##*/}"
    echo "${key}=${value}"
  done > "${ENV_FILE}"

chmod 600 "${ENV_FILE}"
chown qcom:qcom "${ENV_FILE}"
echo "Written ${ENV_FILE}"
