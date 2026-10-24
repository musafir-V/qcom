#!/usr/bin/env bash
# Download the latest Linux amd64 qcom-server published by GitHub Actions
# (workflow publish-binary.yml on main) and install it for systemd.
#
# Usage on an instance (as root):
#   ./scripts/pull-binary.sh
#   systemctl restart qcom

set -euo pipefail

REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"
BUCKET="${QCOM_DEPLOY_BUCKET:-qcom-deploy-assets-078455283887-use1}"
DEST="${QCOM_BINARY_PATH:-/app/qcom/bin/qcom-server}"

mkdir -p "$(dirname "${DEST}")"
aws s3 cp "s3://${BUCKET}/qcom-server" "${DEST}" --region "${REGION}"
chmod 755 "${DEST}"
if id -u qcom &>/dev/null; then
	chown qcom:qcom "${DEST}"
fi

SHA=$(aws s3 cp "s3://${BUCKET}/qcom-server.sha" - --region "${REGION}" 2>/dev/null || true)
echo "Installed ${DEST} (sha=${SHA:-unknown})"
