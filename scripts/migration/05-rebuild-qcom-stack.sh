#!/usr/bin/env bash
# Phase 3A: Rebuild qcom stack in new account with identical resource names.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
ENV_FILE="${ROOT}/.deploy.new.env"
FOUNDATION="${ROOT}/data/migration-foundation.json"

[[ -f "$ENV_FILE" ]] || { echo "Missing .deploy.new.env" >&2; exit 1; }
set -a; source "$ENV_FILE"; set +a
unset AWS_PROFILE AWS_DEFAULT_PROFILE AWS_SESSION_TOKEN

REGION="${AWS_REGION:-ap-southeast-2}"
VPC_ID=$(jq -r '.vpc_id' "$FOUNDATION")
SUBNETS=$(jq -r '.subnet_ids | join(" ")' "$FOUNDATION")
ACCOUNT_ID="${NEW_ACCOUNT_ID:-$(aws sts get-caller-identity --query Account --output text)}"
TABLE="${DYNAMODB_TABLE_NAME:-QComTable}"

ROLE="${QCOM_IAM_ROLE:-qcom-ec2-role}"
PROFILE="${QCOM_IAM_PROFILE:-qcom-ec2-profile}"
ALB_NAME="${QCOM_ALB_NAME:-qcom-alb}"
TG_NAME="${QCOM_TG_NAME:-qcom-tg}"
ASG_NAME="${QCOM_ASG_NAME:-qcom-asg}"
LT_NAME="${QCOM_LT_NAME:-qcom-lt}"
DOMAIN="${QCOM_DOMAIN:-api.bunzodelivery.com}"

echo "=== Phase 3A: qcom stack (${ACCOUNT_ID}) ==="

# 1. IAM
if ! aws iam get-role --role-name "$ROLE" >/dev/null 2>&1; then
  cat > /tmp/ec2-trust-policy.json <<'EOF'
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}
EOF
  aws iam create-role --role-name "$ROLE" --assume-role-policy-document file:///tmp/ec2-trust-policy.json
  aws iam put-role-policy --role-name "$ROLE" --policy-name qcom-ssm-read \
    --policy-document "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":[\"ssm:GetParameter\",\"ssm:GetParametersByPath\",\"ssm:GetParameters\"],\"Resource\":[\"arn:aws:ssm:${REGION}:${ACCOUNT_ID}:parameter/qcom/prod\",\"arn:aws:ssm:${REGION}:${ACCOUNT_ID}:parameter/qcom/prod/*\"]}]}"
  echo "Created IAM role ${ROLE}"
fi

DDB_POLICY=$(cat <<EOF
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": ["dynamodb:GetItem","dynamodb:PutItem","dynamodb:UpdateItem","dynamodb:DeleteItem",
               "dynamodb:Query","dynamodb:Scan","dynamodb:BatchWriteItem","dynamodb:BatchGetItem","dynamodb:DescribeTable"],
    "Resource": ["arn:aws:dynamodb:${REGION}:${ACCOUNT_ID}:table/${TABLE}","arn:aws:dynamodb:${REGION}:${ACCOUNT_ID}:table/${TABLE}/*"]
  }]
}
EOF
)
aws iam put-role-policy --role-name "$ROLE" --policy-name qcom-dynamodb --policy-document "$DDB_POLICY"

CW_POLICY='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["logs:CreateLogGroup","logs:CreateLogStream","logs:PutLogEvents","logs:DescribeLogStreams"],"Resource":"arn:aws:logs:*:*:log-group:/qcom/*"}]}'
aws iam put-role-policy --role-name "$ROLE" --policy-name qcom-cloudwatch-logs --policy-document "$CW_POLICY"

if ! aws iam get-instance-profile --instance-profile-name "$PROFILE" >/dev/null 2>&1; then
  aws iam create-instance-profile --instance-profile-name "$PROFILE"
  aws iam add-role-to-instance-profile --instance-profile-name "$PROFILE" --role-name "$ROLE"
  sleep 10
  echo "Created instance profile ${PROFILE}"
fi

# 2. Security groups
ALB_SG=$(aws ec2 describe-security-groups --filters "Name=group-name,Values=qcom-alb-sg" "Name=vpc-id,Values=${VPC_ID}" \
  --query 'SecurityGroups[0].GroupId' --output text --region "$REGION" 2>/dev/null || echo "None")
if [[ "$ALB_SG" == "None" || -z "$ALB_SG" ]]; then
  ALB_SG=$(aws ec2 create-security-group --group-name qcom-alb-sg --description "ALB SG for qcom" \
    --vpc-id "$VPC_ID" --query GroupId --output text --region "$REGION")
  aws ec2 authorize-security-group-ingress --group-id "$ALB_SG" --protocol tcp --port 80 --cidr 0.0.0.0/0 --region "$REGION"
  aws ec2 authorize-security-group-ingress --group-id "$ALB_SG" --protocol tcp --port 443 --cidr 0.0.0.0/0 --region "$REGION"
  echo "Created ALB SG: ${ALB_SG}"
fi

EC2_SG=$(aws ec2 describe-security-groups --filters "Name=group-name,Values=qcom-ec2-sg" "Name=vpc-id,Values=${VPC_ID}" \
  --query 'SecurityGroups[0].GroupId' --output text --region "$REGION" 2>/dev/null || echo "None")
if [[ "$EC2_SG" == "None" || -z "$EC2_SG" ]]; then
  EC2_SG=$(aws ec2 create-security-group --group-name qcom-ec2-sg --description "qcom EC2 SG" \
    --vpc-id "$VPC_ID" --query GroupId --output text --region "$REGION")
  aws ec2 authorize-security-group-ingress --group-id "$EC2_SG" --protocol tcp --port 8080 \
    --source-group "$ALB_SG" --region "$REGION" 2>/dev/null || true
  aws ec2 authorize-security-group-ingress --group-id "$EC2_SG" --protocol tcp --port 22 \
    --cidr 0.0.0.0/0 --region "$REGION" 2>/dev/null || true
  echo "Created EC2 SG: ${EC2_SG}"
fi

# 3. Target group
TG_ARN=$(aws elbv2 describe-target-groups --names "$TG_NAME" --region "$REGION" \
  --query 'TargetGroups[0].TargetGroupArn' --output text 2>/dev/null || echo "None")
if [[ "$TG_ARN" == "None" || -z "$TG_ARN" ]]; then
  TG_ARN=$(aws elbv2 create-target-group --name "$TG_NAME" --protocol HTTP --port 8080 \
    --vpc-id "$VPC_ID" --health-check-path /health --health-check-interval-seconds 15 \
    --healthy-threshold-count 2 --unhealthy-threshold-count 3 \
    --query 'TargetGroups[0].TargetGroupArn' --output text --region "$REGION")
  echo "Created TG: ${TG_ARN}"
fi

# 4. ALB
ALB_ARN=$(aws elbv2 describe-load-balancers --names "$ALB_NAME" --region "$REGION" \
  --query 'LoadBalancers[0].LoadBalancerArn' --output text 2>/dev/null || echo "None")
if [[ "$ALB_ARN" == "None" || -z "$ALB_ARN" ]]; then
  ALB_ARN=$(aws elbv2 create-load-balancer --name "$ALB_NAME" --subnets $SUBNETS \
    --security-groups "$ALB_SG" --scheme internet-facing --type application \
    --query 'LoadBalancers[0].LoadBalancerArn' --output text --region "$REGION")
  echo "Created ALB: ${ALB_ARN}"
fi
ALB_DNS=$(aws elbv2 describe-load-balancers --load-balancer-arns "$ALB_ARN" --region "$REGION" \
  --query 'LoadBalancers[0].DNSName' --output text)

# 5. HTTP listener (for bootstrap health checks before HTTPS cert is ready)
HTTP_LISTENERS=$(aws elbv2 describe-listeners --load-balancer-arn "$ALB_ARN" --region "$REGION" \
  --query 'Listeners[?Port==`80`].ListenerArn' --output text)
if [[ -z "$HTTP_LISTENERS" || "$HTTP_LISTENERS" == "None" ]]; then
  aws elbv2 create-listener --load-balancer-arn "$ALB_ARN" --protocol HTTP --port 80 \
    --default-actions "[{\"Type\":\"forward\",\"TargetGroupArn\":\"${TG_ARN}\"}]" \
    --region "$REGION" >/dev/null
  echo "Created HTTP:80 listener (temporary — switch to HTTPS redirect after cert)"
fi

# 6. ACM cert
CERT_ARN=$(aws acm list-certificates --region "$REGION" \
  --query "CertificateSummaryList[?DomainName=='${DOMAIN}'].CertificateArn | [0]" --output text)
if [[ "$CERT_ARN" == "None" || -z "$CERT_ARN" ]]; then
  CERT_ARN=$(aws acm request-certificate --domain-name "$DOMAIN" --validation-method DNS \
    --region "$REGION" --query CertificateArn --output text)
  sleep 5
  echo "Requested ACM cert: ${CERT_ARN}"
fi
CERT_STATUS=$(aws acm describe-certificate --certificate-arn "$CERT_ARN" --region "$REGION" \
  --query 'Certificate.Status' --output text)
VALIDATION=$(aws acm describe-certificate --certificate-arn "$CERT_ARN" --region "$REGION" \
  --query 'Certificate.DomainValidationOptions[0].ResourceRecord' --output json 2>/dev/null || echo '{}')

# 7. HTTPS listener if cert issued
HTTPS_LISTENERS=$(aws elbv2 describe-listeners --load-balancer-arn "$ALB_ARN" --region "$REGION" \
  --query 'Listeners[?Port==`443`].ListenerArn' --output text)
if [[ "$CERT_STATUS" == "ISSUED" && ( -z "$HTTPS_LISTENERS" || "$HTTPS_LISTENERS" == "None" ) ]]; then
  HTTPS_ARN=$(aws elbv2 create-listener --load-balancer-arn "$ALB_ARN" --protocol HTTPS --port 443 \
    --certificates "CertificateArn=${CERT_ARN}" \
    --default-actions '[{"Type":"fixed-response","FixedResponseConfig":{"StatusCode":"404","ContentType":"text/plain","MessageBody":"Not found"}}]' \
    --query 'Listeners[0].ListenerArn' --output text --region "$REGION")
  aws elbv2 create-rule --listener-arn "$HTTPS_ARN" --priority 5 \
    --conditions '[{"Field":"path-pattern","Values":["/health"]}]' \
    --actions "[{\"Type\":\"forward\",\"TargetGroupArn\":\"${TG_ARN}\"}]" --region "$REGION" 2>/dev/null || true
  aws elbv2 create-rule --listener-arn "$HTTPS_ARN" --priority 10 \
    --conditions '[{"Field":"path-pattern","Values":["/api/v1/*"]}]' \
    --actions "[{\"Type\":\"forward\",\"TargetGroupArn\":\"${TG_ARN}\"}]" --region "$REGION" 2>/dev/null || true
  echo "Created HTTPS listener + rules"
elif [[ "$CERT_STATUS" != "ISSUED" ]]; then
  echo "ACM cert status: ${CERT_STATUS} — add validation CNAME in Squarespace (see below)"
fi

# 8. Launch template + ASG
# ec2-bootstrap.sh uses apt — Ubuntu 24.04 noble
AMI=$(aws ec2 describe-images --region "$REGION" --owners 099720109477 \
  --filters 'Name=name,Values=ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*' 'Name=state,Values=available' \
  --query 'sort_by(Images,&CreationDate)[-1].ImageId' --output text)
[[ "$AMI" == "None" || -z "$AMI" ]] && { echo "ERROR: no Ubuntu 24.04 AMI found" >&2; exit 1; }

USERDATA=$(base64 < "${ROOT}/scripts/ec2-bootstrap.sh" | tr -d '\n')

if ! aws ec2 describe-launch-templates --launch-template-names "$LT_NAME" --region "$REGION" >/dev/null 2>&1; then
  aws ec2 create-launch-template --launch-template-name "$LT_NAME" --region "$REGION" \
    --launch-template-data "{
      \"ImageId\": \"${AMI}\",
      \"InstanceType\": \"t3.micro\",
      \"UserData\": \"${USERDATA}\",
      \"IamInstanceProfile\": {\"Name\": \"${PROFILE}\"},
      \"SecurityGroupIds\": [\"${EC2_SG}\"],
      \"TagSpecifications\": [{\"ResourceType\":\"instance\",\"Tags\":[{\"Key\":\"Name\",\"Value\":\"qcom-server\"}]}]
    }"
  echo "Created launch template ${LT_NAME} (AMI ${AMI})"
fi
LT_ID=$(aws ec2 describe-launch-templates --launch-template-names "$LT_NAME" --region "$REGION" \
  --query 'LaunchTemplates[0].LaunchTemplateId' --output text)

if ! aws autoscaling describe-auto-scaling-groups --auto-scaling-group-names "$ASG_NAME" --region "$REGION" >/dev/null 2>&1; then
  aws autoscaling create-auto-scaling-group \
    --auto-scaling-group-name "$ASG_NAME" \
    --launch-template "LaunchTemplateId=${LT_ID},Version=\$Latest" \
    --min-size 1 --desired-capacity 1 --max-size 2 \
    --vpc-zone-identifier "$(echo $SUBNETS | tr ' ' ',')" \
    --target-group-arns "$TG_ARN" \
    --health-check-type ELB --health-check-grace-period 300 \
    --region "$REGION"
  echo "Created ASG ${ASG_NAME}"
fi

cat > "${ROOT}/data/migration-phase2.json" <<EOF
{
  "phase": 2,
  "completed_at": "$(date -u +"%Y-%m-%dT%H:%M:%SZ")",
  "dynamodb_items": 955,
  "ssm_params": 16,
  "rds_instance": "quickcommerce",
  "rds_endpoint": "$(cat "${ROOT}/data/rds-new-endpoint.txt" 2>/dev/null || echo quickcommerce.c9se0yaqs4di.ap-southeast-2.rds.amazonaws.com)",
  "rds_database": "inventory"
}
EOF

cat > "${ROOT}/data/migration-phase3a.json" <<EOF
{
  "phase": "3a",
  "completed_at": "$(date -u +"%Y-%m-%dT%H:%M:%SZ")",
  "alb_dns": "${ALB_DNS}",
  "alb_arn": "${ALB_ARN}",
  "target_group_arn": "${TG_ARN}",
  "cert_arn": "${CERT_ARN}",
  "cert_status": "${CERT_STATUS}",
  "acm_validation": ${VALIDATION},
  "asg": "${ASG_NAME}"
}
EOF

echo ""
echo "=== Phase 3A complete ==="
echo "ALB DNS:      ${ALB_DNS}"
echo "ACM status:   ${CERT_STATUS}"
echo "Validation:   ${VALIDATION}"
echo "Waiting for ASG instance bootstrap (~5 min)..."
echo "Test: curl -s http://${ALB_DNS}/health"
