#!/usr/bin/env bash
#
# Installs and configures Grafana Alloy on an Ubuntu qcom host so that it
# scrapes the local qcom metrics endpoint (127.0.0.1:2112/metrics) and
# remote-writes to Grafana Cloud.
#
# Grafana Cloud credentials are read from SSM Parameter Store in the *current*
# AWS account, mirroring qcom's existing /qcom/prod/* convention (the prefix is
# literally /qcom/prod/ even in the staging account).
#
# Idempotent: safe to re-run. Intended to be invoked from the ASG instance
# bootstrap/user-data (prod) and run once by hand on the staging box.
#
# Usage:
#   sudo QCOM_ENV=production ./install-alloy.sh
#   sudo QCOM_ENV=staging    ./install-alloy.sh
#
set -euo pipefail

QCOM_ENV="${QCOM_ENV:?set QCOM_ENV to 'staging' or 'production'}"
AWS_REGION="${AWS_REGION:-ap-southeast-2}"
SSM_PREFIX="${SSM_PREFIX:-/qcom/prod}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ "${EUID}" -ne 0 ]]; then
  echo "install-alloy.sh must run as root (use sudo)" >&2
  exit 1
fi

# 1. Install Alloy from the Grafana apt repository (skipped if already present).
if ! command -v alloy >/dev/null 2>&1; then
  echo "Installing Grafana Alloy..."
  apt-get update
  apt-get install -y gpg wget apt-transport-https
  mkdir -p /etc/apt/keyrings
  wget -q -O - https://apt.grafana.com/gpg.key | gpg --dearmor -o /etc/apt/keyrings/grafana.gpg
  echo "deb [signed-by=/etc/apt/keyrings/grafana.gpg] https://apt.grafana.com stable main" \
    > /etc/apt/sources.list.d/grafana.list
  apt-get update
  apt-get install -y alloy
fi

# 2. Fetch Grafana Cloud remote-write credentials from SSM.
get_param() {
  aws ssm get-parameter --with-decryption --region "${AWS_REGION}" \
    --name "$1" --query 'Parameter.Value' --output text
}

echo "Reading Grafana Cloud credentials from SSM (${SSM_PREFIX}/*)..."
PROM_URL="$(get_param "${SSM_PREFIX}/GRAFANA_CLOUD_PROM_URL")"
PROM_USER="$(get_param "${SSM_PREFIX}/GRAFANA_CLOUD_PROM_USER")"
PROM_KEY="$(get_param "${SSM_PREFIX}/GRAFANA_CLOUD_PROM_API_KEY")"

# 3. Write the systemd EnvironmentFile consumed by the alloy unit + sys.env().
cat > /etc/default/alloy <<EOF
CONFIG_FILE=/etc/alloy/config.alloy
QCOM_ENV=${QCOM_ENV}
GRAFANA_CLOUD_PROM_URL=${PROM_URL}
GRAFANA_CLOUD_PROM_USER=${PROM_USER}
GRAFANA_CLOUD_PROM_API_KEY=${PROM_KEY}
EOF
chmod 600 /etc/default/alloy

# 4. Install the qcom Alloy config and (re)start the service.
install -D -m 644 "${SCRIPT_DIR}/config.alloy" /etc/alloy/config.alloy
systemctl enable alloy
systemctl restart alloy

echo "Alloy installed and running (env=${QCOM_ENV}). Check: systemctl status alloy"
