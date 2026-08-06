#!/usr/bin/env bash
# One-time setup: creates all qcom SSM SecureString parameters.
# Run from your laptop with AWS credentials that have ssm:PutParameter permission.
# Usage: AWS_DEFAULT_REGION=<region> ./scripts/setup-ssm.sh

set -euo pipefail

REGION="${AWS_DEFAULT_REGION:?Set AWS_DEFAULT_REGION (e.g. export AWS_DEFAULT_REGION=ap-south-1)}"

put_param() {
  local name="$1"
  local value="$2"
  echo "Setting ${name}..."
  aws ssm put-parameter \
    --name "${name}" \
    --value "${value}" \
    --type "SecureString" \
    --overwrite \
    --region "${REGION}"
}

echo "=== Setting up SSM parameters for qcom ==="
echo "Enter values when prompted. Leave blank to keep existing value."
echo ""

read -r -p "JWT_SECRET_KEY (min 32 chars): " JWT_SECRET_KEY
read -r -p "DYNAMODB_REGION [ap-southeast-2]: " DYNAMODB_REGION
DYNAMODB_REGION="${DYNAMODB_REGION:-ap-southeast-2}"
read -r -p "DYNAMODB_TABLE_NAME [QComTable]: " DYNAMODB_TABLE_NAME
DYNAMODB_TABLE_NAME="${DYNAMODB_TABLE_NAME:-QComTable}"
read -r -p "S3_REGION [ap-southeast-2]: " S3_REGION
S3_REGION="${S3_REGION:-ap-southeast-2}"
read -r -p "S3_BUCKET [printdrop-documents]: " S3_BUCKET
S3_BUCKET="${S3_BUCKET:-printdrop-documents}"
read -r -p "GOOGLE_MAPS_API_KEY: " GOOGLE_MAPS_API_KEY
read -r -p "TWILIO_ACCOUNT_SID: " TWILIO_ACCOUNT_SID
read -r -p "TWILIO_AUTH_TOKEN: " TWILIO_AUTH_TOKEN
read -r -p "TWILIO_VERIFY_SERVICE_SID: " TWILIO_VERIFY_SERVICE_SID
read -r -p "PORT [8080]: " PORT
PORT="${PORT:-8080}"
read -r -p "GITHUB_DEPLOY_KEY path (path to private key file): " DEPLOY_KEY_PATH

[ -n "${JWT_SECRET_KEY}" ]      && put_param "/qcom/prod/JWT_SECRET_KEY"      "${JWT_SECRET_KEY}"
[ -n "${DYNAMODB_REGION}" ]     && put_param "/qcom/prod/DYNAMODB_REGION"     "${DYNAMODB_REGION}"
[ -n "${DYNAMODB_TABLE_NAME}" ] && put_param "/qcom/prod/DYNAMODB_TABLE_NAME" "${DYNAMODB_TABLE_NAME}"
[ -n "${S3_REGION}" ]           && put_param "/qcom/prod/S3_REGION"           "${S3_REGION}"
[ -n "${S3_BUCKET}" ]           && put_param "/qcom/prod/S3_BUCKET"           "${S3_BUCKET}"
[ -n "${GOOGLE_MAPS_API_KEY}" ] && put_param "/qcom/prod/GOOGLE_MAPS_API_KEY" "${GOOGLE_MAPS_API_KEY}"
[ -n "${TWILIO_ACCOUNT_SID}" ]        && put_param "/qcom/prod/TWILIO_ACCOUNT_SID"        "${TWILIO_ACCOUNT_SID}"
[ -n "${TWILIO_AUTH_TOKEN}" ]         && put_param "/qcom/prod/TWILIO_AUTH_TOKEN"         "${TWILIO_AUTH_TOKEN}"
[ -n "${TWILIO_VERIFY_SERVICE_SID}" ] && put_param "/qcom/prod/TWILIO_VERIFY_SERVICE_SID" "${TWILIO_VERIFY_SERVICE_SID}"
[ -n "${PORT}" ]                      && put_param "/qcom/prod/PORT"                      "${PORT}"

if [ -n "${DEPLOY_KEY_PATH}" ] && [ -f "${DEPLOY_KEY_PATH}" ]; then
  put_param "/qcom/prod/GITHUB_DEPLOY_KEY" "$(cat "${DEPLOY_KEY_PATH}")"
fi

echo ""
echo "=== Done. Verify with: ==="
echo "aws ssm get-parameters-by-path --path /qcom/prod --with-decryption --region ${REGION} --query 'Parameters[*].Name'"
