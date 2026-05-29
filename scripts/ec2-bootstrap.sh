#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# EC2 User Data bootstrap script — runs automatically as root on first boot
# whenever the ASG (qcom-asg) launches a new instance from the Launch Template
# (qcom-lt). It installs all dependencies, clones the repo, compiles the
# binary, loads secrets, and starts the systemd service. You never run this
# manually — AWS runs it for you.
# -----------------------------------------------------------------------------

# Exit immediately if any command fails (-e), treat unset variables as errors
# (-u), and propagate pipe failures (-o pipefail) so silent mid-pipe failures
# don't go unnoticed.
set -euo pipefail

# --- IMDSv2 token (required on Ubuntu 24.04) ----------------------------------
# EC2 Instance Metadata Service v2 requires a session token before you can
# query anything from http://169.254.169.254. We fetch a token valid for 6
# hours (21600 seconds) using a PUT request, then use it for all subsequent
# metadata calls. Without this, metadata requests return 401 on Ubuntu 24.04.
TOKEN=$(curl -s -X PUT "http://169.254.169.254/latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 21600")

# Read the AWS region this instance is running in from the instance metadata.
# Used later when calling the AWS CLI to read secrets from SSM.
REGION=$(curl -s -H "X-aws-ec2-metadata-token: ${TOKEN}" http://169.254.169.254/latest/meta-data/placement/region)

# Pin the Go version so all instances always build with the same toolchain.
GO_VERSION="1.24.3"

# Public GitHub URL — no SSH key or deploy key needed since the repo is public.
REPO_URL="https://github.com/musafir-V/qcom.git"

# Directory where the repo will be cloned on the instance.
APP_DIR="/app/qcom"

# Name of the systemd service unit (must match the filename in deploy/).
SERVICE_NAME="qcom"

# --- Redirect all output to a log file ----------------------------------------
# From this point on, everything printed to stdout or stderr goes to this file.
# If bootstrap fails, SSH into the instance and run:
#   sudo cat /var/log/qcom-bootstrap.log
exec > /var/log/qcom-bootstrap.log 2>&1

echo "=== qcom bootstrap started at $(date) ==="

# --- System packages ----------------------------------------------------------
# Refresh the apt package index so we get current package versions.
apt-get update -y

# Install the tools the rest of this script needs:
#   git   — to clone the repo
#   make  — to run `make build`
#   gcc   — required by some Go packages that use CGo
#   curl  — to download Go and the AWS CLI installer
#   unzip — to extract the AWS CLI zip
apt-get install -y git make gcc curl unzip

# --- AWS CLI v2 ---------------------------------------------------------------
# Ubuntu 24.04 does not ship with the AWS CLI. We install v2 from the official
# Amazon binary. It is needed by fetch-env.sh to read secrets from SSM.
curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o /tmp/awscliv2.zip

# Extract into /tmp (the installer script ends up at /tmp/aws/install).
unzip -q /tmp/awscliv2.zip -d /tmp

# Run the official installer — puts `aws` in /usr/local/bin.
/tmp/aws/install

# Clean up the downloaded files so they don't sit around wasting disk space.
rm -rf /tmp/aws /tmp/awscliv2.zip

# --- App Linux user -----------------------------------------------------------
# Create a dedicated system user `qcom` with no login shell and no home
# directory password. Running the server as a non-root user limits blast radius
# if the process is ever compromised. The `id -u` check makes this idempotent
# so re-running the script doesn't error if the user already exists.
id -u qcom &>/dev/null || useradd -r -s /usr/sbin/nologin -d /app qcom

# Create the /app directory that will hold the repo and the .env file.
mkdir -p /app

# Give the qcom user ownership of /app so it can write the Go module cache,
# compiled binary, and other build artifacts without needing sudo.
chown qcom:qcom /app

# --- Go installation ----------------------------------------------------------
# Only install Go if it isn't already present (makes the script re-runnable
# without reinstalling from scratch).
if ! command -v go &>/dev/null; then
  # Download the official Go tarball for Linux amd64.
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tar.gz

  # Extract into /usr/local — this creates /usr/local/go with the full toolchain.
  tar -C /usr/local -xzf /tmp/go.tar.gz

  # Add Go to PATH for all future login shells by dropping a file in profile.d.
  echo 'export PATH=$PATH:/usr/local/go/bin' > /etc/profile.d/go.sh

  # Also export it into the current shell session so the rest of this script
  # can call `go` without opening a new login shell.
  export PATH=$PATH:/usr/local/go/bin
fi

# --- Clone the repo -----------------------------------------------------------
# Clone as the `qcom` user (not root) so all repo files are owned by qcom
# from the start. Uses HTTPS so no SSH key or GitHub deploy key is needed.
sudo -u qcom git clone "${REPO_URL}" "${APP_DIR}"

# --- Build the binary ---------------------------------------------------------
cd "${APP_DIR}"

# Run `make build` as the qcom user with:
#   HOME=/app      — Go uses $HOME to locate the module cache; /app is qcom's home
#   PATH=...       — include /usr/local/go/bin so the make process can find `go`
#   GOPATH=/app/go — store downloaded module dependencies under /app/go so the
#                    qcom user has write access (avoids permission errors)
#
# The output binary ends up at /app/qcom/bin/qcom-server.
sudo -u qcom HOME=/app PATH=$PATH:/usr/local/go/bin GOPATH=/app/go make build

# --- Load secrets from SSM ----------------------------------------------------
# fetch-env.sh reads all parameters under /qcom/prod/ from AWS SSM Parameter
# Store and writes them as KEY=VALUE lines to /app/.env. The EC2 IAM Instance
# Profile (qcom-ec2-profile) grants the instance read access to those params —
# no AWS credentials need to be stored on disk. The resulting /app/.env is
# chmod 600 and owned by qcom:qcom.
bash "${APP_DIR}/scripts/fetch-env.sh"

# --- Install and start the systemd service ------------------------------------
# Copy the unit file from the repo into the systemd directory. This means the
# service definition is version-controlled alongside the code.
cp "${APP_DIR}/deploy/qcom.service" /etc/systemd/system/qcom.service

# Tell systemd to re-read unit files from disk so it picks up the new file.
systemctl daemon-reload

# Enable the service so it starts automatically on every subsequent reboot.
systemctl enable qcom

# Start the service right now — reads /app/.env, launches qcom-server on :8080.
systemctl start qcom

# --- journald on-disk retention -----------------------------------------------
# By default journald keeps logs until the disk is ~10% full, which can exhaust
# a t3.micro root volume over time. We use a drop-in config (rather than
# overwriting the system journald.conf) so our settings layer cleanly on top.
#
#   SystemMaxUse=200M    — journald will never use more than 200 MB of disk for
#                          persistent logs, regardless of how much log volume the
#                          app produces.
#   MaxRetentionSec=2d   — entries older than 2 days are dropped automatically,
#                          even if total disk usage is below 200 MB. This is the
#                          time-based ceiling; whichever limit is hit first wins.
mkdir -p /etc/systemd/journald.conf.d
cat > /etc/systemd/journald.conf.d/qcom.conf << 'EOF'
[Journal]
SystemMaxUse=200M
MaxRetentionSec=2d
EOF

# Restart journald to apply the new limits. Existing log entries are not lost;
# journald will just enforce the limits going forward and on next vacuum cycle.
systemctl restart systemd-journald

# --- CloudWatch agent ---------------------------------------------------------
# The CloudWatch agent ships journald logs for the qcom.service unit to the
# /qcom/production log group in CloudWatch. This means logs survive instance
# termination and are searchable without needing to SSH in.
#
# Download the official Debian package from Amazon's S3 distribution bucket.
curl -fsSL \
  "https://amazoncloudwatch-agent.s3.amazonaws.com/ubuntu/amd64/latest/amazon-cloudwatch-agent.deb" \
  -o /tmp/amazon-cloudwatch-agent.deb

# Install the package — puts the agent binary at
# /opt/aws/amazon-cloudwatch-agent/bin/amazon-cloudwatch-agent.
dpkg -i /tmp/amazon-cloudwatch-agent.deb
rm -f /tmp/amazon-cloudwatch-agent.deb

# Copy the agent config from the repo into the location the agent expects.
# The config (deploy/amazon-cloudwatch-agent.json) tells the agent:
#   - which log group to write to (/qcom/production)
#   - how to name each stream ({instance_id}/qcom-server)
#   - which journald unit to filter on (qcom.service only)
cp "${APP_DIR}/deploy/amazon-cloudwatch-agent.json" \
  /opt/aws/amazon-cloudwatch-agent/etc/amazon-cloudwatch-agent.json

# Start the agent and tell it to load the config file we just copied.
#   -a fetch-config  — load (or reload) config from the given source
#   -m ec2           — running on EC2, so use instance metadata for region/ID
#   -s               — start the agent after loading config
#   -c file:...      — config source is a local file
/opt/aws/amazon-cloudwatch-agent/bin/amazon-cloudwatch-agent-ctl \
  -a fetch-config \
  -m ec2 \
  -s \
  -c file:/opt/aws/amazon-cloudwatch-agent/etc/amazon-cloudwatch-agent.json

echo "=== qcom bootstrap complete at $(date) ==="
