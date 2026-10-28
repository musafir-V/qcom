#!/usr/bin/env bash
# Deploy qcom to production by replacing each InService instance without
# dropping below the current number of healthy ALB targets. For every old
# instance the script raises desired capacity by one, waits until that extra
# instance is healthy in the load balancer, then terminates the old instance
# and decrements desired capacity back. New instances bootstrap from GitHub
# (main) and pull the published binary from S3 via ec2-bootstrap.sh.
#
# Prerequisites: .deploy.local.env at repo root (see .deploy.local.env.example).
# Usage: ./scripts/deploy.sh   or   make deploy

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
ENV_FILE="${REPO_ROOT}/.deploy.local.env"

if [[ -f "${ENV_FILE}" ]]; then
	set -a
	# shellcheck source=/dev/null
	source "${ENV_FILE}"
	set +a
else
	echo "Missing ${ENV_FILE}. Copy from .deploy.local.env.example and add AWS keys." >&2
	exit 1
fi

REGION="${AWS_REGION:-ap-southeast-2}"
ASG="${QCOM_ASG_NAME:-qcom-asg}"
HEALTH_URL="${QCOM_HEALTH_URL:-https://api.bunzodelivery.com/health}"
WAIT_ATTEMPTS="${DEPLOY_WAIT_ATTEMPTS:-60}"
WAIT_INTERVAL="${DEPLOY_WAIT_INTERVAL:-15}"

if [[ -z "${AWS_ACCESS_KEY_ID:-}" || -z "${AWS_SECRET_ACCESS_KEY:-}" ]]; then
	echo "AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY must be set in .deploy.local.env" >&2
	exit 1
fi

wait_healthy() {
	local tg_arn="$1"
	local desired="$2"
	echo "Waiting for ${desired} healthy target(s) in ALB..."
	for ((i = 1; i <= WAIT_ATTEMPTS; i++)); do
		local healthy in_service
		healthy=$(aws elbv2 describe-target-health \
			--target-group-arn "${tg_arn}" \
			--region "${REGION}" \
			--query 'length(TargetHealthDescriptions[?TargetHealth.State==`healthy`])' \
			--output text)
		in_service=$(aws autoscaling describe-auto-scaling-groups \
			--auto-scaling-group-names "${ASG}" \
			--region "${REGION}" \
			--query 'length(AutoScalingGroups[0].Instances[?LifecycleState==`InService`])' \
			--output text)
		echo "  attempt ${i}/${WAIT_ATTEMPTS}: healthy=${healthy} in_service=${in_service}"
		if [[ "${healthy}" -ge "${desired}" && "${in_service}" -ge "${desired}" ]]; then
			echo "All targets healthy."
			return 0
		fi
		sleep "${WAIT_INTERVAL}"
	done
	echo "ERROR: timed out waiting for healthy targets" >&2
	return 1
}

echo "=== Deploying qcom via zero-downtime ASG replacement ==="
echo "Region: ${REGION}  ASG: ${ASG}"

TG_ARN=$(aws autoscaling describe-auto-scaling-groups \
	--auto-scaling-group-names "${ASG}" \
	--region "${REGION}" \
	--query 'AutoScalingGroups[0].TargetGroupARNs[0]' \
	--output text)

DESIRED=$(aws autoscaling describe-auto-scaling-groups \
	--auto-scaling-group-names "${ASG}" \
	--region "${REGION}" \
	--query 'AutoScalingGroups[0].DesiredCapacity' \
	--output text)

if [[ -z "${TG_ARN}" || "${TG_ARN}" == "None" ]]; then
	echo "ERROR: no target group found for ASG ${ASG}" >&2
	exit 1
fi

INSTANCE_IDS=$(aws autoscaling describe-auto-scaling-groups \
	--auto-scaling-group-names "${ASG}" \
	--region "${REGION}" \
	--query 'AutoScalingGroups[0].Instances[?LifecycleState==`InService`].InstanceId' \
	--output text)

if [[ -z "${INSTANCE_IDS}" ]]; then
	echo "ERROR: no InService instances found in ${ASG}" >&2
	exit 1
fi

echo "Replacing instance(s) without dropping healthy capacity: ${INSTANCE_IDS}"

MAX_SIZE=$(aws autoscaling describe-auto-scaling-groups \
	--auto-scaling-group-names "${ASG}" \
	--region "${REGION}" \
	--query 'AutoScalingGroups[0].MaxSize' \
	--output text)

ORIG_MAX=""
restore_max() {
	if [[ -n "${ORIG_MAX}" ]]; then
		echo "Restoring MaxSize to ${ORIG_MAX}"
		aws autoscaling update-auto-scaling-group \
			--auto-scaling-group-name "${ASG}" \
			--max-size "${ORIG_MAX}" \
			--region "${REGION}" || true
	fi
}
trap restore_max EXIT

# One spare slot so the replacement can join before the old instance leaves.
if [[ "${MAX_SIZE}" -lt $((DESIRED + 1)) ]]; then
	ORIG_MAX="${MAX_SIZE}"
	NEW_MAX=$((DESIRED + 1))
	echo "Raising MaxSize ${MAX_SIZE} -> ${NEW_MAX} for the surge"
	aws autoscaling update-auto-scaling-group \
		--auto-scaling-group-name "${ASG}" \
		--max-size "${NEW_MAX}" \
		--region "${REGION}"
fi

for id in ${INSTANCE_IDS}; do
	CURRENT=$(aws autoscaling describe-auto-scaling-groups \
		--auto-scaling-group-names "${ASG}" \
		--region "${REGION}" \
		--query 'AutoScalingGroups[0].DesiredCapacity' \
		--output text)
	SURGE=$((CURRENT + 1))
	echo "=== Launching replacement before terminating ${id} (desired ${CURRENT} -> ${SURGE}) ==="
	aws autoscaling set-desired-capacity \
		--auto-scaling-group-name "${ASG}" \
		--desired-capacity "${SURGE}" \
		--no-honor-cooldown \
		--region "${REGION}"
	if ! wait_healthy "${TG_ARN}" "${SURGE}"; then
		echo "ERROR: replacement did not become healthy. ${id} is still serving. Desired capacity left at ${SURGE}." >&2
		exit 1
	fi
	echo "=== Terminating ${id} now that the replacement is healthy ==="
	aws autoscaling terminate-instance-in-auto-scaling-group \
		--instance-id "${id}" \
		--should-decrement-desired-capacity \
		--region "${REGION}"
	wait_healthy "${TG_ARN}" "${CURRENT}"
done

echo "=== Verifying ${HEALTH_URL} ==="
health=$(curl -sf "${HEALTH_URL}" || true)
if [[ "${health}" != "OK" ]]; then
	echo "ERROR: health check failed (got: ${health:-<empty>})" >&2
	exit 1
fi

echo "Health check OK"
echo "=== Deploy complete ==="
