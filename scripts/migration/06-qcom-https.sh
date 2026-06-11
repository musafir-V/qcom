#!/usr/bin/env bash
# Attach HTTPS listener to qcom-alb once ACM cert is ISSUED.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
source "${ROOT}/.deploy.new.env"
unset AWS_PROFILE AWS_DEFAULT_PROFILE AWS_SESSION_TOKEN
REGION="${AWS_REGION:-ap-southeast-2}"
DOMAIN="${QCOM_DOMAIN:-api.bunzodelivery.com}"
ALB_NAME="${QCOM_ALB_NAME:-qcom-alb}"
TG_NAME="${QCOM_TG_NAME:-qcom-tg}"

CERT_ARN=$(aws acm list-certificates --region "$REGION" \
  --query "CertificateSummaryList[?DomainName=='${DOMAIN}'].CertificateArn | [0]" --output text)
STATUS=$(aws acm describe-certificate --certificate-arn "$CERT_ARN" --region "$REGION" \
  --query 'Certificate.Status' --output text)

if [[ "$STATUS" != "ISSUED" ]]; then
  echo "ACM cert not ISSUED yet (status=${STATUS})."
  aws acm describe-certificate --certificate-arn "$CERT_ARN" --region "$REGION" \
    --query 'Certificate.DomainValidationOptions[0].ResourceRecord' --output table
  exit 1
fi

ALB_ARN=$(aws elbv2 describe-load-balancers --names "$ALB_NAME" --region "$REGION" \
  --query 'LoadBalancers[0].LoadBalancerArn' --output text)
TG_ARN=$(aws elbv2 describe-target-groups --names "$TG_NAME" --region "$REGION" \
  --query 'TargetGroups[0].TargetGroupArn' --output text)

HTTPS=$(aws elbv2 describe-listeners --load-balancer-arn "$ALB_ARN" --region "$REGION" \
  --query 'Listeners[?Port==`443`].ListenerArn' --output text)
if [[ -z "$HTTPS" || "$HTTPS" == "None" ]]; then
  HTTPS=$(aws elbv2 create-listener --load-balancer-arn "$ALB_ARN" --protocol HTTPS --port 443 \
    --certificates "CertificateArn=${CERT_ARN}" \
    --default-actions '[{"Type":"fixed-response","FixedResponseConfig":{"StatusCode":"404","ContentType":"text/plain","MessageBody":"Not found"}}]' \
    --query 'Listeners[0].ListenerArn' --output text --region "$REGION")
  aws elbv2 create-rule --listener-arn "$HTTPS" --priority 5 \
    --conditions '[{"Field":"path-pattern","Values":["/health"]}]' \
    --actions "[{\"Type\":\"forward\",\"TargetGroupArn\":\"${TG_ARN}\"}]" --region "$REGION"
  aws elbv2 create-rule --listener-arn "$HTTPS" --priority 10 \
    --conditions '[{"Field":"path-pattern","Values":["/api/v1/*"]}]' \
    --actions "[{\"Type\":\"forward\",\"TargetGroupArn\":\"${TG_ARN}\"}]" --region "$REGION"
  # HTTP → HTTPS redirect
  HTTP_ARN=$(aws elbv2 describe-listeners --load-balancer-arn "$ALB_ARN" --region "$REGION" \
    --query 'Listeners[?Port==`80`].ListenerArn' --output text)
  aws elbv2 modify-listener --listener-arn "$HTTP_ARN" --region "$REGION" \
    --default-actions '[{"Type":"redirect","RedirectConfig":{"Protocol":"HTTPS","Port":"443","StatusCode":"HTTP_301"}}]'
  echo "HTTPS listener created"
else
  echo "HTTPS listener already exists"
fi

ALB_DNS=$(aws elbv2 describe-load-balancers --load-balancer-arns "$ALB_ARN" --region "$REGION" \
  --query 'LoadBalancers[0].DNSName' --output text)
echo "qcom ALB: ${ALB_DNS}"
curl -sk "https://${ALB_DNS}/health" && echo
