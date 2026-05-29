#!/usr/bin/env bash
# EC2 User Data script. Runs as root on first boot.
# Installs Go, clones repo, builds binary, installs and starts systemd service.

set -euo pipefail

REGION=$(curl -s http://169.254.169.254/latest/meta-data/placement/region)
GO_VERSION="1.24.3"
REPO_URL="https://github.com/musafir-V/qcom.git"
APP_DIR="/app/qcom"
SERVICE_NAME="qcom"

exec > /var/log/qcom-bootstrap.log 2>&1

echo "=== qcom bootstrap started at $(date) ==="

# --- System setup ---
apt-get update -y
apt-get install -y git make gcc curl unzip

# --- Install AWS CLI v2 ---
curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o /tmp/awscliv2.zip
unzip -q /tmp/awscliv2.zip -d /tmp
/tmp/aws/install
rm -rf /tmp/aws /tmp/awscliv2.zip

# --- Create app user ---
id -u qcom &>/dev/null || useradd -r -s /usr/sbin/nologin -d /app qcom
mkdir -p /app
chown qcom:qcom /app

# --- Install Go ---
if ! command -v go &>/dev/null; then
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tar.gz
  tar -C /usr/local -xzf /tmp/go.tar.gz
  echo 'export PATH=$PATH:/usr/local/go/bin' > /etc/profile.d/go.sh
  export PATH=$PATH:/usr/local/go/bin
fi

# --- Clone repo (public repo, no deploy key needed) ---
sudo -u qcom git clone "${REPO_URL}" "${APP_DIR}"

# --- Build binary ---
cd "${APP_DIR}"
sudo -u qcom HOME=/app PATH=$PATH:/usr/local/go/bin GOPATH=/app/go make build

# --- Fetch env vars from SSM ---
bash "${APP_DIR}/scripts/fetch-env.sh"

# --- Install systemd service ---
cp "${APP_DIR}/deploy/qcom.service" /etc/systemd/system/qcom.service
systemctl daemon-reload
systemctl enable qcom
systemctl start qcom

echo "=== qcom bootstrap complete at $(date) ==="
