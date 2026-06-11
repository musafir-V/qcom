#!/usr/bin/env bash
# Phase 3B: Migrate product-service via AMI (no SSH needed). Updates RDS JDBC host only.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OLD_ENV="${ROOT}/.deploy.local.env"
NEW_ENV="${ROOT}/.deploy.new.env"
source "$OLD_ENV"; source "$NEW_ENV"
unset AWS_PROFILE AWS_DEFAULT_PROFILE AWS_SESSION_TOKEN

REGION="${AWS_REGION:-ap-southeast-2}"
OLD_INSTANCE="${OLD_PRODUCT_INSTANCE_ID:-i-00dc197caba8ab3eb}"
NEW_ACCOUNT="${NEW_ACCOUNT_ID}"
OLD_RDS_HOST="${OLD_RDS_HOST:-quickcommerce.crys0s8colmh.ap-southeast-2.rds.amazonaws.com}"
NEW_RDS_HOST="${NEW_RDS_HOST:-quickcommerce.c9se0yaqs4di.ap-southeast-2.rds.amazonaws.com}"
AMI_NAME="product-service-migration-$(date +%Y%m%d)"
INSTANCE_NAME="${PRODUCT_EC2_NAME:-product-service}"

echo "=== Phase 3B: product-service AMI migration ==="

# Check if already launched in new account
EXISTING=$(aws ec2 describe-instances --region "$REGION" \
  --filters "Name=tag:Name,Values=${INSTANCE_NAME}" "Name=instance-state-name,Values=running,pending" \
  --query 'Reservations[0].Instances[0].InstanceId' --output text 2>/dev/null || echo "None")
if [[ -n "$EXISTING" && "$EXISTING" != "None" ]]; then
  echo "product-service already running: ${EXISTING}"
  echo "${EXISTING}" > "${ROOT}/data/product-service-instance-id.txt"
  exit 0
fi

echo "Creating AMI from old instance ${OLD_INSTANCE}..."
(
  set -a; source "$OLD_ENV"; set +a
  unset AWS_PROFILE AWS_SESSION_TOKEN
  aws ec2 create-image --instance-id "$OLD_INSTANCE" --name "$AMI_NAME" \
    --description "product-service migration" --no-reboot --region "$REGION" \
    --query 'ImageId' --output text > "${ROOT}/data/product-ami-id.txt"
)
OLD_AMI=$(cat "${ROOT}/data/product-ami-id.txt")
echo "AMI: ${OLD_AMI} — waiting..."
(
  set -a; source "$OLD_ENV"; set +a
  unset AWS_PROFILE AWS_SESSION_TOKEN
  aws ec2 wait image-available --image-ids "$OLD_AMI" --region "$REGION"
  aws ec2 modify-image-attribute --image-id "$OLD_AMI" --launch-permission \
    "Add=[{UserId=${NEW_ACCOUNT}}]" --region "$REGION"
)

echo "Copying AMI to new account..."
(
  set -a; source "$NEW_ENV"; set +a
  unset AWS_PROFILE AWS_SESSION_TOKEN
  OLD_ACCOUNT="${OLD_ACCOUNT_ID:-119312949433}"
  NEW_AMI=$(aws ec2 copy-image \
    --source-image-id "$OLD_AMI" \
    --source-region "$REGION" \
    --name "${AMI_NAME}-copy" \
    --region "$REGION" \
    --query 'ImageId' --output text)
  echo "$NEW_AMI" > "${ROOT}/data/product-ami-new.txt"
  aws ec2 wait image-available --image-ids "$NEW_AMI" --region "$REGION"
)

NEW_AMI=$(cat "${ROOT}/data/product-ami-new.txt")
VPC_ID=$(jq -r '.vpc_id' "${ROOT}/data/migration-foundation.json")
SUBNET=$(jq -r '.subnet_ids[0]' "${ROOT}/data/migration-foundation.json")

# User-data: swap RDS endpoint in common Spring config locations, restart java service
USERDATA=$(cat <<EOF | base64 | tr -d '\n'
#!/bin/bash
OLD="${OLD_RDS_HOST}"
NEW="${NEW_RDS_HOST}"
for f in \$(find /app /opt /home -type f \\( -name 'application*.properties' -o -name 'application*.yml' -o -name '.env' \\) 2>/dev/null); do
  sed -i "s|\${OLD}|\${NEW}|g" "\$f" 2>/dev/null || true
done
for svc in product-service java-app; do
  systemctl restart "\$svc" 2>/dev/null || true
done
# Restart any java process managed by systemd
systemctl list-units --type=service --state=running | grep -i java | awk '{print \$1}' | xargs -r systemctl restart
EOF
)

echo "Launching product-service from AMI ${NEW_AMI}..."
(
  set -a; source "$NEW_ENV"; set +a
  unset AWS_PROFILE AWS_SESSION_TOKEN
  # Reuse qcom-ec2-sg pattern or create product SG
  PROD_SG=$(aws ec2 describe-security-groups --filters "Name=group-name,Values=product-ec2-sg" "Name=vpc-id,Values=${VPC_ID}" \
    --query 'SecurityGroups[0].GroupId' --output text --region "$REGION" 2>/dev/null || echo "None")
  if [[ "$PROD_SG" == "None" || -z "$PROD_SG" ]]; then
    PROD_SG=$(aws ec2 create-security-group --group-name product-ec2-sg --description "product-service EC2" \
      --vpc-id "$VPC_ID" --query GroupId --output text --region "$REGION")
    aws ec2 authorize-security-group-ingress --group-id "$PROD_SG" --protocol tcp --port 22 \
      --cidr 0.0.0.0/0 --region "$REGION" 2>/dev/null || true
    aws ec2 authorize-security-group-ingress --group-id "$PROD_SG" --protocol tcp --port 8082 \
      --cidr 0.0.0.0/0 --region "$REGION" 2>/dev/null || true
  fi
  NEW_ID=$(aws ec2 run-instances --image-id "$NEW_AMI" --instance-type t3.medium \
    --subnet-id "$SUBNET" --security-group-ids "$PROD_SG" \
    --user-data "$USERDATA" \
    --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=${INSTANCE_NAME}}]" \
    --query 'Instances[0].InstanceId' --output text --region "$REGION")
  echo "$NEW_ID" > "${ROOT}/data/product-service-instance-id.txt"
  aws ec2 wait instance-running --instance-ids "$NEW_ID" --region "$REGION"
  echo "Launched: ${NEW_ID}"
)

NEW_ID=$(cat "${ROOT}/data/product-service-instance-id.txt")
echo "Running setup-payment-tls.sh for payment-alb..."
(
  set -a; source "$NEW_ENV"; set +a
  unset AWS_PROFILE AWS_SESSION_TOKEN
  INSTANCE_ID="$NEW_ID" AWS_REGION="$REGION" \
    bash "${ROOT}/scripts/setup-payment-tls.sh" request-cert
  INSTANCE_ID="$NEW_ID" AWS_REGION="$REGION" \
    bash "${ROOT}/scripts/setup-payment-tls.sh" security-groups
  INSTANCE_ID="$NEW_ID" AWS_REGION="$REGION" \
    bash "${ROOT}/scripts/setup-payment-tls.sh" target-group
  INSTANCE_ID="$NEW_ID" AWS_REGION="$REGION" \
    bash "${ROOT}/scripts/setup-payment-tls.sh" alb
  # listeners need cert ISSUED — try anyway
  INSTANCE_ID="$NEW_ID" AWS_REGION="$REGION" \
    bash "${ROOT}/scripts/setup-payment-tls.sh" listeners 2>/dev/null || \
    echo "WARN: HTTPS listeners need ACM cert ISSUED (add Squarespace CNAME)"
)

echo "product-service instance: ${NEW_ID}"
bash "${ROOT}/scripts/setup-payment-tls.sh" status 2>/dev/null || true
