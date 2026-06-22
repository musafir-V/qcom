#!/usr/bin/env bash
# Expose qcom /internal/* on the public qcom-alb for local testing.
# Production order-service should still use http://internal.bunzodelivery.com:8080 (VPC-only NLB).
#
# Note: ALB * does not match slashes — use /internal/* not /internal/v1/*
# Usage: source .deploy.local.env && ./scripts/enable-public-internal-alb.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
ENV_FILE="${REPO_ROOT}/.deploy.local.env"

if [[ -f "${ENV_FILE}" ]]; then
	set -a
	# shellcheck source=/dev/null
	source "${ENV_FILE}"
	set +a
fi

REGION="${AWS_REGION:-ap-southeast-2}"
PRIORITY="${PUBLIC_INTERNAL_RULE_PRIORITY:-8}"
PATH_PATTERN="${PUBLIC_INTERNAL_PATH_PATTERN:-/internal/*}"

ALB_ARN=$(aws elbv2 describe-load-balancers --names qcom-alb --region "${REGION}" \
	--query 'LoadBalancers[0].LoadBalancerArn' --output text)
LISTENER=$(aws elbv2 describe-listeners --load-balancer-arn "${ALB_ARN}" --region "${REGION}" \
	--query 'Listeners[?Port==`443`].ListenerArn' --output text)
TG_ARN=$(aws elbv2 describe-target-groups --names qcom-tg --region "${REGION}" \
	--query 'TargetGroups[0].TargetGroupArn' --output text)

EXISTING=$(aws elbv2 describe-rules --listener-arn "${LISTENER}" --region "${REGION}" --output json \
	| python3 -c "import sys,json; rules=json.load(sys.stdin)['Rules'];
print(next((r['RuleArn'] for r in rules if not r['IsDefault'] and any(
  c.get('Field')=='path-pattern' and '${PATH_PATTERN}' in c.get('Values',[]) for c in r.get('Conditions',[]))), ''))")

if [[ -n "${EXISTING}" ]]; then
	echo "Updating existing rule ${EXISTING} → ${PATH_PATTERN}"
	aws elbv2 modify-rule --rule-arn "${EXISTING}" --region "${REGION}" \
		--conditions "Field=path-pattern,Values=${PATH_PATTERN}" \
		--actions "Type=forward,TargetGroupArn=${TG_ARN}"
else
	echo "Creating rule priority ${PRIORITY}: ${PATH_PATTERN} → qcom-tg"
	aws elbv2 create-rule --listener-arn "${LISTENER}" --priority "${PRIORITY}" \
		--region "${REGION}" \
		--conditions "Field=path-pattern,Values=${PATH_PATTERN}" \
		--actions "Type=forward,TargetGroupArn=${TG_ARN}"
fi

echo ""
echo "Public test URL:"
echo "  POST https://api.bunzodelivery.com/internal/v1/notifications/send"
echo ""
echo "Verify:"
echo "  curl -s -X POST https://api.bunzodelivery.com/internal/v1/notifications/send -H 'Content-Type: application/json' -d '{}'"
