# EC2 Deployment Infrastructure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the manual git-pull-and-run workflow with a fully scripted, systemd-managed deployment on EC2 backed by ASG (min=1) and an ALB with path-based routing.

**Architecture:** A single EC2 instance runs the qcom Go binary as a systemd service, loading secrets from SSM Parameter Store at boot. An ASG (min=1) replaces the instance automatically if it dies. An ALB sits in front, routing `/api/v1/*` traffic to the EC2 instance via a Target Group. Day-to-day deploys are SSH-in → git pull → make build → systemctl restart.

**Tech Stack:** Go 1.24, systemd, AWS EC2, ASG, ALB, SSM Parameter Store, ACM, Squarespace DNS

---

## Files

| Action | Path | Responsibility |
|---|---|---|
| Create | `deploy/qcom.service` | systemd unit — runs binary, loads env, restarts on crash |
| Create | `scripts/fetch-env.sh` | Runs on EC2 — fetches SSM params, writes `/app/.env` |
| Create | `scripts/ec2-bootstrap.sh` | EC2 User Data — installs Go, clones repo, wires systemd |
| Create | `scripts/deploy.sh` | Local deploy script — SSH in, pull, build, restart |
| Create | `scripts/setup-ssm.sh` | One-time — creates all SSM SecureString parameters |
| Modify | `Makefile` | Add `deploy` target |

---

## Task 1: Create the systemd unit file

**Files:**
- Create: `deploy/qcom.service`

- [ ] **Step 1: Create the unit file**

```ini
# deploy/qcom.service
[Unit]
Description=qcom API server
After=network.target
Wants=network-online.target

[Service]
Type=simple
User=qcom
Group=qcom
WorkingDirectory=/app/qcom
EnvironmentFile=/app/.env
ExecStart=/app/qcom/bin/qcom-server
Restart=on-failure
RestartSec=5s
StandardOutput=journal
StandardError=journal
SyslogIdentifier=qcom

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 2: Verify the file exists**

```bash
cat deploy/qcom.service
```

Expected: unit file content printed with no errors.

- [ ] **Step 3: Commit**

```bash
git add deploy/qcom.service
git commit -m "deploy: add systemd unit file for qcom service"
```

---

## Task 2: Create the SSM env-fetch script (runs on EC2 at boot and deploy)

This script fetches every secret from SSM Parameter Store and writes `/app/.env`. It runs during bootstrap and before each deploy restart.

**Files:**
- Create: `scripts/fetch-env.sh`

- [ ] **Step 1: Create the script**

```bash
#!/usr/bin/env bash
# scripts/fetch-env.sh
# Fetches all qcom production secrets from SSM Parameter Store and writes /app/.env.
# Requires the EC2 IAM Instance Profile to have ssm:GetParametersByPath permission on /qcom/prod/*.

set -euo pipefail

SSM_PREFIX="/qcom/prod"
ENV_FILE="/app/.env"
REGION=$(curl -s http://169.254.169.254/latest/meta-data/placement/region)

echo "Fetching config from SSM prefix ${SSM_PREFIX} in region ${REGION}..."

# Fetch all parameters under /qcom/prod/ as key=value pairs
aws ssm get-parameters-by-path \
  --path "${SSM_PREFIX}" \
  --with-decryption \
  --region "${REGION}" \
  --query "Parameters[*].[Name,Value]" \
  --output text | while IFS=$'\t' read -r name value; do
    key="${name##*/}"   # strip /qcom/prod/ prefix, keep just the key name
    echo "${key}=${value}"
  done > "${ENV_FILE}"

chmod 600 "${ENV_FILE}"
chown qcom:qcom "${ENV_FILE}"
echo "Written ${ENV_FILE}"
```

- [ ] **Step 2: Make executable and commit**

```bash
chmod +x scripts/fetch-env.sh
git add scripts/fetch-env.sh
git commit -m "deploy: add SSM env-fetch script"
```

---

## Task 3: Create the EC2 bootstrap script (User Data)

This runs once when a new EC2 instance launches. It installs Go, clones the repo using a deploy key from SSM, builds the binary, installs the systemd service, fetches env vars, and starts the service.

**Files:**
- Create: `scripts/ec2-bootstrap.sh`

- [ ] **Step 1: Create the script**

Replace `YOUR_GITHUB_REPO_URL` with your actual GitHub SSH URL (e.g. `git@github.com:musafir-V/qcom.git`).

```bash
#!/usr/bin/env bash
# scripts/ec2-bootstrap.sh
# EC2 User Data script. Runs as root on first boot.
# Installs Go, clones repo, builds binary, installs and starts systemd service.

set -euo pipefail

REGION=$(curl -s http://169.254.169.254/latest/meta-data/placement/region)
GO_VERSION="1.24.3"
REPO_URL="git@github.com:musafir-V/qcom.git"
APP_DIR="/app/qcom"
ENV_FILE="/app/.env"
SERVICE_NAME="qcom"

exec > /var/log/qcom-bootstrap.log 2>&1

echo "=== qcom bootstrap started at $(date) ==="

# --- System setup ---
yum update -y
yum install -y git make gcc

# --- Create app user ---
id -u qcom &>/dev/null || useradd -r -s /sbin/nologin -d /app qcom
mkdir -p /app
chown qcom:qcom /app

# --- Install Go ---
if ! command -v go &>/dev/null; then
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tar.gz
  tar -C /usr/local -xzf /tmp/go.tar.gz
  echo 'export PATH=$PATH:/usr/local/go/bin' > /etc/profile.d/go.sh
  export PATH=$PATH:/usr/local/go/bin
fi

# --- Fetch GitHub deploy key from SSM ---
mkdir -p /home/qcom/.ssh
chmod 700 /home/qcom/.ssh
aws ssm get-parameter \
  --name "/qcom/prod/GITHUB_DEPLOY_KEY" \
  --with-decryption \
  --region "${REGION}" \
  --query "Parameter.Value" \
  --output text > /home/qcom/.ssh/id_ed25519
chmod 600 /home/qcom/.ssh/id_ed25519
chown -R qcom:qcom /home/qcom/.ssh

# Trust github.com host key
ssh-keyscan github.com >> /home/qcom/.ssh/known_hosts
chown qcom:qcom /home/qcom/.ssh/known_hosts

# --- Clone repo ---
sudo -u qcom GIT_SSH_COMMAND="ssh -i /home/qcom/.ssh/id_ed25519" \
  git clone "${REPO_URL}" "${APP_DIR}"

# --- Build binary ---
cd "${APP_DIR}"
sudo -u qcom HOME=/home/qcom PATH=$PATH:/usr/local/go/bin make build

# --- Fetch env vars from SSM ---
bash /app/qcom/scripts/fetch-env.sh

# --- Install systemd service ---
cp "${APP_DIR}/deploy/qcom.service" /etc/systemd/system/qcom.service
systemctl daemon-reload
systemctl enable qcom
systemctl start qcom

echo "=== qcom bootstrap complete at $(date) ==="
```

- [ ] **Step 2: Make executable and commit**

```bash
chmod +x scripts/ec2-bootstrap.sh
git add scripts/ec2-bootstrap.sh
git commit -m "deploy: add EC2 bootstrap / User Data script"
```

---

## Task 4: Create the local SSH deploy script

This is the script you run from your laptop to deploy new code. It SSHes into the EC2 instance, pulls latest master, rebuilds the binary, refreshes env vars from SSM, and restarts the service.

**Files:**
- Create: `scripts/deploy.sh`

- [ ] **Step 1: Create the script**

Replace `EC2_HOST` with your instance's IP or DNS. Replace `EC2_KEY` with the path to your EC2 SSH key.

```bash
#!/usr/bin/env bash
# scripts/deploy.sh
# Usage: ./scripts/deploy.sh [ec2-host] [ssh-key-path]
# Deploys the latest master to the running EC2 instance.

set -euo pipefail

EC2_HOST="${1:-${QCOM_EC2_HOST:?Set QCOM_EC2_HOST or pass host as first arg}}"
EC2_KEY="${2:-${QCOM_EC2_KEY:?Set QCOM_EC2_KEY or pass key path as second arg}}"

SSH="ssh -i ${EC2_KEY} -o StrictHostKeyChecking=no ec2-user@${EC2_HOST}"

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
```

- [ ] **Step 2: Make executable and commit**

```bash
chmod +x scripts/deploy.sh
git add scripts/deploy.sh
git commit -m "deploy: add local SSH deploy script"
```

---

## Task 5: Add Makefile deploy target

**Files:**
- Modify: `Makefile`

- [ ] **Step 1: Add deploy target to Makefile**

Add this block after the existing `clean` target in `Makefile`:

```makefile
deploy: ## Deploy latest master to EC2 (usage: make deploy HOST=<ip> KEY=<path>)
	@if [ -z "$(HOST)" ] || [ -z "$(KEY)" ]; then \
		echo "Usage: make deploy HOST=<ec2-ip> KEY=<path-to-pem>"; \
		exit 1; \
	fi
	@QCOM_EC2_HOST=$(HOST) QCOM_EC2_KEY=$(KEY) ./scripts/deploy.sh
```

- [ ] **Step 2: Verify**

```bash
make help | grep deploy
```

Expected: `deploy` appears in the help output.

- [ ] **Step 3: Commit**

```bash
git add Makefile
git commit -m "deploy: add make deploy target"
```

---

## Task 6: Create the SSM parameters setup script (one-time)

This is a one-time script you run from your laptop to populate SSM Parameter Store with all production secrets. Run it once; the bootstrap and deploy scripts read from SSM automatically after that.

**Files:**
- Create: `scripts/setup-ssm.sh`

- [ ] **Step 1: Create the script**

```bash
#!/usr/bin/env bash
# scripts/setup-ssm.sh
# One-time setup: creates all qcom SSM SecureString parameters.
# Run from your laptop with AWS credentials that have ssm:PutParameter permission.
# Usage: ./scripts/setup-ssm.sh

set -euo pipefail

REGION="${AWS_DEFAULT_REGION:?Set AWS_DEFAULT_REGION}"

put_param() {
  local name="$1"
  local value="$2"
  echo "Setting ${name}..."
  aws ssm put-parameter \
    --name "${name}" \
    --value "${value}" \
    --type "SecureString" \
    --overwrite \
    --region "${REGION}"
}

echo "=== Setting up SSM parameters for qcom ==="
echo "Enter values when prompted. Leave blank to skip (keeps existing value)."
echo ""

read -r -p "JWT_SECRET_KEY (min 32 chars): " JWT_SECRET_KEY
read -r -p "DYNAMODB_REGION [ap-south-1]: " DYNAMODB_REGION
DYNAMODB_REGION="${DYNAMODB_REGION:-ap-south-1}"
read -r -p "DYNAMODB_TABLE_NAME [QComTable]: " DYNAMODB_TABLE_NAME
DYNAMODB_TABLE_NAME="${DYNAMODB_TABLE_NAME:-QComTable}"
read -r -p "S3_REGION [ap-southeast-2]: " S3_REGION
S3_REGION="${S3_REGION:-ap-southeast-2}"
read -r -p "S3_BUCKET [printdrop-documents]: " S3_BUCKET
S3_BUCKET="${S3_BUCKET:-printdrop-documents}"
read -r -p "GOOGLE_MAPS_API_KEY: " GOOGLE_MAPS_API_KEY
read -r -p "GITHUB_DEPLOY_KEY path (paste path to private key file): " DEPLOY_KEY_PATH
read -r -p "PORT [8080]: " PORT
PORT="${PORT:-8080}"

[ -n "${JWT_SECRET_KEY}" ]        && put_param "/qcom/prod/JWT_SECRET_KEY"       "${JWT_SECRET_KEY}"
[ -n "${DYNAMODB_REGION}" ]       && put_param "/qcom/prod/DYNAMODB_REGION"      "${DYNAMODB_REGION}"
[ -n "${DYNAMODB_TABLE_NAME}" ]   && put_param "/qcom/prod/DYNAMODB_TABLE_NAME"  "${DYNAMODB_TABLE_NAME}"
[ -n "${S3_REGION}" ]             && put_param "/qcom/prod/S3_REGION"            "${S3_REGION}"
[ -n "${S3_BUCKET}" ]             && put_param "/qcom/prod/S3_BUCKET"            "${S3_BUCKET}"
[ -n "${GOOGLE_MAPS_API_KEY}" ]   && put_param "/qcom/prod/GOOGLE_MAPS_API_KEY"  "${GOOGLE_MAPS_API_KEY}"
[ -n "${PORT}" ]                  && put_param "/qcom/prod/PORT"                 "${PORT}"

if [ -n "${DEPLOY_KEY_PATH}" ] && [ -f "${DEPLOY_KEY_PATH}" ]; then
  put_param "/qcom/prod/GITHUB_DEPLOY_KEY" "$(cat "${DEPLOY_KEY_PATH}")"
fi

echo ""
echo "=== SSM parameters set. Verify with: ==="
echo "aws ssm get-parameters-by-path --path /qcom/prod --with-decryption --region ${REGION} --query 'Parameters[*].Name'"
```

- [ ] **Step 2: Make executable and commit**

```bash
chmod +x scripts/setup-ssm.sh
git add scripts/setup-ssm.sh
git commit -m "deploy: add one-time SSM parameter setup script"
```

---

## Task 7: IAM Instance Profile setup (AWS CLI — run once from laptop)

The EC2 instance needs permission to read SSM parameters. This creates an IAM role, attaches the policy, and creates the Instance Profile.

- [ ] **Step 1: Create IAM role trust policy file**

```bash
cat > /tmp/ec2-trust-policy.json << 'EOF'
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": { "Service": "ec2.amazonaws.com" },
    "Action": "sts:AssumeRole"
  }]
}
EOF
```

- [ ] **Step 2: Create IAM role**

```bash
aws iam create-role \
  --role-name qcom-ec2-role \
  --assume-role-policy-document file:///tmp/ec2-trust-policy.json
```

Expected: JSON output with role ARN like `arn:aws:iam::ACCOUNT:role/qcom-ec2-role`

- [ ] **Step 3: Create inline policy to read SSM params**

```bash
cat > /tmp/qcom-ssm-policy.json << 'EOF'
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": [
      "ssm:GetParameter",
      "ssm:GetParametersByPath"
    ],
    "Resource": "arn:aws:ssm:*:*:parameter/qcom/prod/*"
  }]
}
EOF

aws iam put-role-policy \
  --role-name qcom-ec2-role \
  --policy-name qcom-ssm-read \
  --policy-document file:///tmp/qcom-ssm-policy.json
```

- [ ] **Step 4: Create Instance Profile and attach role**

```bash
aws iam create-instance-profile --instance-profile-name qcom-ec2-profile

aws iam add-role-to-instance-profile \
  --instance-profile-name qcom-ec2-profile \
  --role-name qcom-ec2-role
```

- [ ] **Step 5: Verify**

```bash
aws iam get-instance-profile --instance-profile-name qcom-ec2-profile \
  --query "InstanceProfile.Roles[0].RoleName" --output text
```

Expected: `qcom-ec2-role`

---

## Task 8: ALB Listener Rule setup (AWS CLI — run once from laptop)

Adds the path-based routing rule so the ALB forwards `/api/v1/*` traffic to your EC2 Target Group.

You need two values from your existing AWS setup before running these commands:
- `LISTENER_ARN` — your ALB's HTTPS listener ARN. Find it: `aws elbv2 describe-listeners --load-balancer-arn <your-alb-arn> --query "Listeners[?Port==\`443\`].ListenerArn" --output text`
- `TARGET_GROUP_ARN` — your EC2 Target Group ARN. Find it: `aws elbv2 describe-target-groups --query "TargetGroups[?TargetGroupName=='qcom'].TargetGroupArn" --output text`

- [ ] **Step 1: Add path-based listener rule**

```bash
LISTENER_ARN="<your-listener-arn>"
TARGET_GROUP_ARN="<your-target-group-arn>"
REGION="<your-region>"

aws elbv2 create-rule \
  --listener-arn "${LISTENER_ARN}" \
  --priority 10 \
  --conditions '[{"Field":"path-pattern","Values":["/api/v1/*"]}]' \
  --actions "[{\"Type\":\"forward\",\"TargetGroupArn\":\"${TARGET_GROUP_ARN}\"}]" \
  --region "${REGION}"
```

Expected: JSON output with `RuleArn` and `IsDefault: false`.

- [ ] **Step 2: Add health check rule for `/health`**

```bash
aws elbv2 create-rule \
  --listener-arn "${LISTENER_ARN}" \
  --priority 5 \
  --conditions '[{"Field":"path-pattern","Values":["/health"]}]' \
  --actions "[{\"Type\":\"forward\",\"TargetGroupArn\":\"${TARGET_GROUP_ARN}\"}]" \
  --region "${REGION}"
```

- [ ] **Step 3: Verify rules**

```bash
aws elbv2 describe-rules --listener-arn "${LISTENER_ARN}" \
  --query "Rules[*].{Priority:Priority,Path:Conditions[0].Values[0]}" \
  --output table
```

Expected: table showing priority 5 → `/health`, priority 10 → `/api/v1/*`.

---

## Task 9: DNS setup (manual — Squarespace)

- [ ] **Step 1: Request ACM certificate**

```bash
REGION="<your-region>"
DOMAIN="api.yourdomain.com"

aws acm request-certificate \
  --domain-name "${DOMAIN}" \
  --validation-method DNS \
  --region "${REGION}"
```

Note the `CertificateArn` in the output.

- [ ] **Step 2: Get the DNS validation CNAME**

```bash
CERT_ARN="<certificate-arn-from-step-1>"

aws acm describe-certificate \
  --certificate-arn "${CERT_ARN}" \
  --region "${REGION}" \
  --query "Certificate.DomainValidationOptions[0].ResourceRecord"
```

Expected output: `{ "Name": "_abc123.api.yourdomain.com.", "Type": "CNAME", "Value": "_xyz456.acm-validations.aws." }`

- [ ] **Step 3: Add validation CNAME in Squarespace**

1. Go to Squarespace → Domains → your domain → DNS Settings
2. Add CNAME record:
   - Host: `_abc123.api` (strip your root domain and trailing dot from the Name field)
   - Points to: `_xyz456.acm-validations.aws` (strip trailing dot from Value field)
3. Save. Wait 5–15 minutes for ACM to validate and issue the cert.

- [ ] **Step 4: Attach cert to ALB HTTPS listener**

```bash
aws elbv2 add-listener-certificates \
  --listener-arn "${LISTENER_ARN}" \
  --certificates "CertificateArn=${CERT_ARN}" \
  --region "${REGION}"
```

- [ ] **Step 5: Add API subdomain CNAME in Squarespace**

Get your ALB DNS name:
```bash
aws elbv2 describe-load-balancers \
  --query "LoadBalancers[*].DNSName" --output text --region "${REGION}"
```

In Squarespace DNS Settings, add:
- Host: `api`
- Type: CNAME
- Points to: `<your-alb-dns-name>.elb.amazonaws.com`

- [ ] **Step 6: Verify**

```bash
curl -s https://api.yourdomain.com/health
```

Expected: HTTP 200 with empty body.

---

## Task 10: Launch Template update (AWS CLI — links bootstrap script to ASG)

Updates your existing Launch Template to include the bootstrap User Data script and the IAM Instance Profile, so every new ASG instance self-configures.

- [ ] **Step 1: Encode bootstrap script to base64**

```bash
USERDATA=$(base64 -i scripts/ec2-bootstrap.sh)
echo "${USERDATA}" | head -c 100
```

Expected: base64-encoded string (starts with `IyEvdXNy` for `#!/usr`).

- [ ] **Step 2: Create new Launch Template version**

```bash
LAUNCH_TEMPLATE_ID="<your-launch-template-id>"
REGION="<your-region>"

aws ec2 create-launch-template-version \
  --launch-template-id "${LAUNCH_TEMPLATE_ID}" \
  --source-version '$Latest' \
  --launch-template-data "{
    \"UserData\": \"${USERDATA}\",
    \"IamInstanceProfile\": {\"Name\": \"qcom-ec2-profile\"}
  }" \
  --region "${REGION}"
```

Expected: JSON with `VersionNumber` (e.g. `2`).

- [ ] **Step 3: Set new version as default**

```bash
VERSION="<version-number-from-step-2>"

aws ec2 modify-launch-template \
  --launch-template-id "${LAUNCH_TEMPLATE_ID}" \
  --default-version "${VERSION}" \
  --region "${REGION}"
```

- [ ] **Step 4: Verify by triggering a test replacement**

Terminate your current EC2 instance — ASG will bring up a new one using the updated Launch Template:

```bash
INSTANCE_ID=$(aws ec2 describe-instances \
  --filters "Name=tag:aws:autoscaling:groupName,Values=<your-asg-name>" \
  --query "Reservations[0].Instances[0].InstanceId" --output text \
  --region "${REGION}")

aws ec2 terminate-instances --instance-ids "${INSTANCE_ID}" --region "${REGION}"
```

Watch the new instance come up (~3-5 min) and pass ALB health checks:

```bash
watch -n 10 "aws elbv2 describe-target-health \
  --target-group-arn ${TARGET_GROUP_ARN} --region ${REGION} \
  --query 'TargetHealthDescriptions[*].{Id:Target.Id,State:TargetHealth.State}' \
  --output table"
```

Expected: new instance transitions from `initial` → `healthy`.

- [ ] **Step 5: Final smoke test**

```bash
curl -s https://api.yourdomain.com/health
```

Expected: HTTP 200.

---

## Ongoing Deploy Workflow (after all tasks complete)

```bash
# Push code to master as usual
git push origin master

# Deploy to production
make deploy HOST=<ec2-ip> KEY=~/.ssh/your-key.pem
```

The deploy takes ~30 seconds: git pull + Go compile + service restart.
