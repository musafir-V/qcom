#!/usr/bin/env bash
# Usage: ./scripts/deploy.sh [ec2-host] [ssh-key-path]
# Or:    QCOM_EC2_HOST=<ip> QCOM_EC2_KEY=<path> ./scripts/deploy.sh
# Deploys the latest master to the running EC2 instance.

set -euo pipefail

EC2_HOST="${1:-${QCOM_EC2_HOST:?Set QCOM_EC2_HOST or pass host as first arg}}"
EC2_KEY="${2:-${QCOM_EC2_KEY:?Set QCOM_EC2_KEY or pass key path as second arg}}"

SSH="ssh -i ${EC2_KEY} -o StrictHostKeyChecking=no ubuntu@${EC2_HOST}"

echo "=== Deploying qcom to ${EC2_HOST} ==="

echo "--- Pulling latest master ---"
$SSH "cd /app/qcom && git pull origin master"

echo "--- Rebuilding binary ---"
$SSH "cd /app/qcom && PATH=\$PATH:/usr/local/go/bin make build"

echo "--- Refreshing env from SSM ---"
$SSH "sudo bash /app/qcom/scripts/fetch-env.sh"

echo "--- Restarting service ---"
$SSH "sudo systemctl restart qcom"

echo "--- Checking service status ---"
$SSH "sudo systemctl status qcom --no-pager"

echo "=== Deploy complete ==="
