#!/usr/bin/env bash
# Short cloud-init user-data: gunzip bootstrap and run in background (avoids 120s timeout).
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
B64=$(gzip -c "${ROOT}/scripts/ec2-bootstrap.sh" | base64 | tr -d '\n')
cat <<STUB
#!/bin/bash
echo '${B64}' | base64 -d | gunzip > /tmp/qcom-bootstrap.sh
chmod +x /tmp/qcom-bootstrap.sh
nohup /tmp/qcom-bootstrap.sh > /var/log/qcom-bootstrap-nohup.log 2>&1 &
STUB
