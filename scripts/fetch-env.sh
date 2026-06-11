#!/usr/bin/env bash
# Fetches all qcom production secrets from SSM Parameter Store and writes /app/.env.
# Requires the EC2 IAM Instance Profile to have ssm:GetParametersByPath on /qcom/prod/*.

set -euo pipefail

SSM_PREFIX="/qcom/prod"
ENV_FILE="/app/.env"
TOKEN=$(curl -s -X PUT "http://169.254.169.254/latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 21600")
REGION=$(curl -s -H "X-aws-ec2-metadata-token: ${TOKEN}" http://169.254.169.254/latest/meta-data/placement/region)

echo "Fetching config from SSM prefix ${SSM_PREFIX} in region ${REGION}..."

: > "${ENV_FILE}"
NEXT=""
while true; do
  ARGS=(--path "${SSM_PREFIX}" --with-decryption --recursive --region "${REGION}")
  [[ -n "${NEXT}" ]] && ARGS+=(--starting-token "${NEXT}")
  PAGE=$(aws ssm get-parameters-by-path "${ARGS[@]}" --output json)
  echo "${PAGE}" | jq -r '.Parameters[] | "\(.Name)\t\(.Value)"' | while IFS=$'\t' read -r name value; do
    key="${name##*/}"
    echo "${key}=${value}"
  done >> "${ENV_FILE}"
  NEXT=$(echo "${PAGE}" | jq -r '.NextToken // empty')
  [[ -z "${NEXT}" ]] && break
done

chmod 600 "${ENV_FILE}"
chown qcom:qcom "${ENV_FILE}"
echo "Written ${ENV_FILE}"
