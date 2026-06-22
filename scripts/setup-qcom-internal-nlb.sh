#!/usr/bin/env bash
# Private VPC path for order-service → qcom internal API (Option B).
#
# Creates:
#   - internal NLB (qcom-internal-nlb) :8080
#   - target group (qcom-internal-tg) → qcom ASG
#   - SG rule: product-ec2-sg → qcom-ec2-sg:8080
#   - SG rule: VPC CIDR → qcom-ec2-sg:8080 (NLB health checks)
#   - Route53 private zone internal.bunzodelivery.com → NLB (VPC-only DNS)
#
# Usage: source .deploy.local.env && ./scripts/setup-qcom-internal-nlb.sh
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
ASG_NAME="${QCOM_ASG_NAME:-qcom-asg}"
NLB_NAME="${QCOM_INTERNAL_NLB_NAME:-qcom-internal-nlb}"
TG_NAME="${QCOM_INTERNAL_TG_NAME:-qcom-internal-tg}"
PRIVATE_ZONE="${QCOM_INTERNAL_DNS_ZONE:-internal.bunzodelivery.com}"
PRODUCT_SG_NAME="${PRODUCT_EC2_SG_NAME:-product-ec2-sg}"
QCOM_SG_NAME="${QCOM_EC2_SG_NAME:-qcom-ec2-sg}"

echo "=== qcom internal NLB setup (region=${REGION}) ==="

SUBNETS_RAW=$(aws autoscaling describe-auto-scaling-groups \
	--auto-scaling-group-names "${ASG_NAME}" \
	--region "${REGION}" \
	--query 'AutoScalingGroups[0].VPCZoneIdentifier' --output text)
SUBNETS=${SUBNETS_RAW//,/ }

FIRST_SUBNET=${SUBNETS_RAW%%,*}
VPC_ID=$(aws ec2 describe-subnets --subnet-ids "${FIRST_SUBNET}" --region "${REGION}" \
	--query 'Subnets[0].VpcId' --output text)

VPC_CIDR=$(aws ec2 describe-vpcs --vpc-ids "${VPC_ID}" --region "${REGION}" \
	--query 'Vpcs[0].CidrBlock' --output text)

echo "VPC: ${VPC_ID} (${VPC_CIDR})"
echo "Subnets: ${SUBNETS}"

# --- Target group (TCP — NLB) -----------------------------------------------
TG_ARN=$(aws elbv2 describe-target-groups --names "${TG_NAME}" --region "${REGION}" \
	--query 'TargetGroups[0].TargetGroupArn' --output text 2>/dev/null || echo "None")

if [[ "${TG_ARN}" == "None" || -z "${TG_ARN}" ]]; then
	echo "Creating target group ${TG_NAME}..."
	TG_ARN=$(aws elbv2 create-target-group \
		--name "${TG_NAME}" \
		--protocol TCP \
		--port 8080 \
		--vpc-id "${VPC_ID}" \
		--target-type instance \
		--health-check-protocol HTTP \
		--health-check-port 8080 \
		--health-check-path /health \
		--healthy-threshold-count 2 \
		--unhealthy-threshold-count 2 \
		--health-check-interval-seconds 30 \
		--region "${REGION}" \
		--query 'TargetGroups[0].TargetGroupArn' --output text)
	echo "Created TG: ${TG_ARN}"
else
	echo "Target group exists: ${TG_ARN}"
fi

# --- Internal NLB -------------------------------------------------------------
NLB_ARN=$(aws elbv2 describe-load-balancers --names "${NLB_NAME}" --region "${REGION}" \
	--query 'LoadBalancers[0].LoadBalancerArn' --output text 2>/dev/null || echo "None")

if [[ "${NLB_ARN}" == "None" || -z "${NLB_ARN}" ]]; then
	echo "Creating internal NLB ${NLB_NAME}..."
	NLB_ARN=$(aws elbv2 create-load-balancer \
		--name "${NLB_NAME}" \
		--type network \
		--scheme internal \
		--subnets ${SUBNETS} \
		--region "${REGION}" \
		--query 'LoadBalancers[0].LoadBalancerArn' --output text)
	echo "Waiting for NLB to become active..."
	aws elbv2 wait load-balancer-available --load-balancer-arns "${NLB_ARN}" --region "${REGION}"
else
	echo "NLB exists: ${NLB_ARN}"
fi

NLB_DNS=$(aws elbv2 describe-load-balancers --load-balancer-arns "${NLB_ARN}" --region "${REGION}" \
	--query 'LoadBalancers[0].DNSName' --output text)
NLB_HOSTED_ZONE=$(aws elbv2 describe-load-balancers --load-balancer-arns "${NLB_ARN}" --region "${REGION}" \
	--query 'LoadBalancers[0].CanonicalHostedZoneId' --output text)

LISTENER=$(aws elbv2 describe-listeners --load-balancer-arn "${NLB_ARN}" --region "${REGION}" \
	--query 'Listeners[?Port==`8080`].ListenerArn' --output text 2>/dev/null || true)

if [[ -z "${LISTENER}" || "${LISTENER}" == "None" ]]; then
	echo "Creating NLB listener TCP :8080..."
	aws elbv2 create-listener \
		--load-balancer-arn "${NLB_ARN}" \
		--protocol TCP \
		--port 8080 \
		--default-actions "Type=forward,TargetGroupArn=${TG_ARN}" \
		--region "${REGION}" >/dev/null
fi

# --- Attach ASG to internal TG ------------------------------------------------
ATTACHED=$(aws autoscaling describe-auto-scaling-groups \
	--auto-scaling-group-names "${ASG_NAME}" \
	--region "${REGION}" \
	--query "AutoScalingGroups[0].TargetGroupARNs[?@=='${TG_ARN}'] | length(@)" --output text)

if [[ "${ATTACHED}" == "0" ]]; then
	echo "Attaching ${ASG_NAME} to ${TG_NAME}..."
	aws autoscaling attach-load-balancer-target-groups \
		--auto-scaling-group-name "${ASG_NAME}" \
		--target-group-arns "${TG_ARN}" \
		--region "${REGION}"
else
	echo "ASG already attached to internal TG"
fi

# --- Security groups ----------------------------------------------------------
PRODUCT_SG=$(aws ec2 describe-security-groups \
	--filters "Name=group-name,Values=${PRODUCT_SG_NAME}" "Name=vpc-id,Values=${VPC_ID}" \
	--region "${REGION}" --query 'SecurityGroups[0].GroupId' --output text)

QCOM_SG=$(aws ec2 describe-security-groups \
	--filters "Name=group-name,Values=${QCOM_SG_NAME}" "Name=vpc-id,Values=${VPC_ID}" \
	--region "${REGION}" --query 'SecurityGroups[0].GroupId' --output text)

echo "product-ec2-sg: ${PRODUCT_SG}"
echo "qcom-ec2-sg:    ${QCOM_SG}"

authorize_sg() {
	local sg="$1" port="$2" desc="$3"
	shift 3
	if aws ec2 authorize-security-group-ingress \
		--group-id "${sg}" --protocol tcp --port "${port}" "$@" \
		--region "${REGION}" 2>/dev/null; then
		echo "SG rule added: ${desc}"
	else
		echo "SG rule already present (or failed): ${desc}"
	fi
}

# App traffic from order-service EC2 (preserve_client_ip on NLB)
authorize_sg "${QCOM_SG}" 8080 "product-ec2-sg → qcom:8080" \
	--source-group "${PRODUCT_SG}"

# NLB health checks originate from VPC private addresses
authorize_sg "${QCOM_SG}" 8080 "VPC CIDR → qcom:8080 (NLB health checks)" \
	--cidr "${VPC_CIDR}"

# --- Route53 private hosted zone -----------------------------------------------
ZONE_ID=$(aws route53 list-hosted-zones-by-name --dns-name "${PRIVATE_ZONE}." \
	--query "HostedZones[?Name=='${PRIVATE_ZONE}.'].Id" --output text 2>/dev/null | sed 's|/hostedzone/||')

if [[ -z "${ZONE_ID}" ]]; then
	echo "Creating private hosted zone ${PRIVATE_ZONE}..."
	ZONE_ID=$(aws route53 create-hosted-zone \
		--name "${PRIVATE_ZONE}" \
		--vpc "VPCRegion=${REGION},VPCId=${VPC_ID}" \
		--caller-reference "qcom-internal-$(date +%s)" \
		--hosted-zone-config "Comment=Private qcom internal API,PrivateZone=true" \
		--query 'HostedZone.Id' --output text | sed 's|/hostedzone/||')
else
	echo "Private zone exists: ${ZONE_ID}"
	# Ensure VPC association (idempotent-ish)
	aws route53 create-vpc-association-authorization \
		--hosted-zone-id "${ZONE_ID}" --vpc "VPCRegion=${REGION},VPCId=${VPC_ID}" \
		--region "${REGION}" 2>/dev/null || true
	aws route53 associate-vpc-with-hosted-zone \
		--hosted-zone-id "${ZONE_ID}" --vpc "VPCRegion=${REGION},VPCId=${VPC_ID}" \
		--region "${REGION}" 2>/dev/null || true
fi

CHANGE_BATCH=$(cat <<EOF
{
  "Comment": "qcom internal NLB apex alias",
  "Changes": [{
    "Action": "UPSERT",
    "ResourceRecordSet": {
      "Name": "${PRIVATE_ZONE}.",
      "Type": "A",
      "AliasTarget": {
        "HostedZoneId": "${NLB_HOSTED_ZONE}",
        "DNSName": "dualstack.${NLB_DNS}.",
        "EvaluateTargetHealth": true
      }
    }
  }]
}
EOF
)

aws route53 change-resource-record-sets \
	--hosted-zone-id "${ZONE_ID}" \
	--change-batch "${CHANGE_BATCH}" >/dev/null

echo ""
echo "=== Done ==="
echo "Internal URL (VPC-only):  http://${PRIVATE_ZONE}:8080"
echo "Notification endpoint:    http://${PRIVATE_ZONE}:8080/internal/v1/notifications/send"
echo "NLB DNS (fallback):       ${NLB_DNS}:8080"
echo ""
echo "order-service env (on product EC2 /app/.env or docker-compose):"
echo "  QCOM_NOTIFICATION_ENABLED=true"
echo "  QCOM_NOTIFICATION_BASE_URL=http://${PRIVATE_ZONE}:8080"
echo ""
echo "Waiting for internal target health..."
for i in $(seq 1 20); do
	HEALTHY=$(aws elbv2 describe-target-health \
		--target-group-arn "${TG_ARN}" \
		--region "${REGION}" \
		--query 'length(TargetHealthDescriptions[?TargetHealth.State==`healthy`])' \
		--output text)
	echo "  attempt ${i}: healthy=${HEALTHY}"
	if [[ "${HEALTHY}" -ge 1 ]]; then
		break
	fi
	sleep 15
done

aws elbv2 describe-target-health --target-group-arn "${TG_ARN}" --region "${REGION}" \
	--query 'TargetHealthDescriptions[*].{Target:Target.Id,State:TargetHealth.State,Reason:TargetHealth.Reason}' \
	--output table
