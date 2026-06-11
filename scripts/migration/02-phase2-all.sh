#!/usr/bin/env bash
# Run Phase 2 data migrations (DynamoDB, RDS, SSM) in parallel.
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
PIDS=()
NAMES=()

run_agent() {
  local name="$1" script="$2"
  echo "Starting ${name}..."
  bash "$script" > "${DIR}/../../data/phase2-${name}.log" 2>&1 &
  PIDS+=($!)
  NAMES+=("$name")
}

run_agent "dynamodb" "${DIR}/02-migrate-dynamodb.sh"
run_agent "rds" "${DIR}/03-migrate-rds.sh"
run_agent "ssm" "${DIR}/04-migrate-ssm.sh"

FAIL=0
for i in "${!PIDS[@]}"; do
  if wait "${PIDS[$i]}"; then
    echo "PASS: ${NAMES[$i]}"
  else
    echo "FAIL: ${NAMES[$i]} — see data/phase2-${NAMES[$i]}.log"
    FAIL=1
  fi
done

[[ $FAIL -eq 0 ]] || exit 1
echo "=== Phase 2 complete ==="
