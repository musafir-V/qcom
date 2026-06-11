#!/usr/bin/env bash
# Enumerate all AWS resources in the current account/region.
# Usage: source .deploy.local.env && ./scripts/migration/00-inventory.sh
set -euo pipefail

REGION="${AWS_REGION:-ap-southeast-2}"
OUT="${1:-data/migration-manifest.json}"
mkdir -p "$(dirname "$OUT")"

ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
TS=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

echo "Inventorying account ${ACCOUNT_ID} in ${REGION}..."

jarr() { aws "$@" --region "$REGION" --output json 2>/dev/null || echo '[]'; }

SSM_PARAMS=$(aws ssm describe-parameters --region "$REGION" \
  --query 'Parameters[*].Name' --output json 2>/dev/null || echo '[]')

cat > "$OUT" <<EOF
{
  "generated_at": "${TS}",
  "account_id": "${ACCOUNT_ID}",
  "region": "${REGION}",
  "ec2_instances": $(jarr ec2 describe-instances),
  "auto_scaling_groups": $(jarr autoscaling describe-auto-scaling-groups),
  "launch_templates": $(jarr ec2 describe-launch-templates),
  "load_balancers": $(jarr elbv2 describe-load-balancers),
  "target_groups": $(jarr elbv2 describe-target-groups),
  "dynamodb_tables": $(jarr dynamodb list-tables),
  "s3_buckets": $(jarr s3api list-buckets),
  "rds_instances": $(jarr rds describe-db-instances),
  "rds_clusters": $(jarr rds describe-db-clusters),
  "rds_snapshots": $(jarr rds describe-db-snapshots --snapshot-type manual),
  "ssm_parameter_names": ${SSM_PARAMS},
  "iam_roles": $(jarr iam list-roles --query 'Roles[?contains(RoleName, `qcom`) || contains(RoleName, `product`) || contains(RoleName, `payment`) || contains(RoleName, `bunzo`)]'),
  "acm_certificates": $(jarr acm list-certificates),
  "security_groups": $(jarr ec2 describe-security-groups --query 'SecurityGroups[?contains(GroupName, `qcom`) || contains(GroupName, `product`) || contains(GroupName, `payment`) || contains(GroupName, `bunzo`)]'),
  "vpcs": $(jarr ec2 describe-vpcs),
  "subnets": $(jarr ec2 describe-subnets),
  "cloudwatch_log_groups": $(jarr logs describe-log-groups),
  "sns_topics": $(jarr sns list-topics),
  "sqs_queues": $(jarr sqs list-queues),
  "lambda_functions": $(jarr lambda list-functions),
  "route53_zones": $(jarr route53 list-hosted-zones),
  "kms_keys": $(jarr kms list-keys)
}
EOF

echo ""
echo "=== Summary ==="
echo "Account:  ${ACCOUNT_ID}"
echo "EC2:      $(jq '[.Reservations[].Instances[]] | length' "$OUT")"
echo "ASG:      $(jq '.AutoScalingGroups | length' "$OUT")"
echo "ALB:      $(jq '.LoadBalancers | length' "$OUT")"
echo "DDB:      $(jq '.TableNames | length' "$OUT")"
echo "S3:       $(jq '.Buckets | length' "$OUT")"
echo "RDS:      $(jq '.DBInstances | length' "$OUT")"
echo "SSM:      $(jq '.ssm_parameter_names | length' "$OUT")"
echo "ACM:      $(jq '.CertificateSummaryList | length' "$OUT")"
echo ""
echo "Manifest: ${OUT}"
