# Production Infrastructure Reference

This document describes the complete production infrastructure for the `qcom` service — what was built, why each piece exists, all resource IDs, and how to recover when things break.

---

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [AWS Resources](#aws-resources)
3. [Deploy Workflow](#deploy-workflow)
4. [How a New Instance Bootstraps Itself](#how-a-new-instance-bootstraps-itself)
5. [Secrets Management](#secrets-management)
6. [Files Added to This Repo](#files-added-to-this-repo)
7. [Step-by-Step: What Was Built and Why](#step-by-step-what-was-built-and-why)
8. [Troubleshooting Runbook](#troubleshooting-runbook)
9. [How to Rebuild from Scratch](#how-to-rebuild-from-scratch)

---

## Architecture Overview

```
Internet
    │
    ▼
Route 53 / Squarespace DNS
api.bunzodelivery.com → qcom-alb-1081983549.ap-southeast-2.elb.amazonaws.com
    │
    ▼
ALB: qcom-alb  (internet-facing, ap-southeast-2)
  Port 80  → HTTP 301 redirect to HTTPS
  Port 443 → path-based routing:
               /health      → qcom-tg (Target Group)
               /api/v1/*    → qcom-tg (Target Group)
               everything else → 404
    │
    ▼
Target Group: qcom-tg
  Protocol: HTTP, Port: 8080
  Health check: GET /health every 15s
    │
    ▼
EC2 Instance (managed by ASG: qcom-asg)
  The Go binary runs as a systemd service on port 8080
  Secrets loaded from SSM Parameter Store at boot
    │
    ▼
AWS DynamoDB + S3
  (pre-existing, not managed by this setup)
```

**Key design decisions:**

- **No Docker / ECS / Kubernetes.** The Go binary is compiled directly on the EC2 instance and run via systemd. Go produces a single self-contained binary — no container overhead needed.
- **Code is built on the instance, not locally.** On deploy, you SSH in, `git pull`, and `make build`. On first boot, the User Data script clones the repo and builds automatically. This means every instance always runs the code it pulled itself — no binary transfer needed.
- **systemd manages the process.** If the Go binary crashes, systemd restarts it in 5 seconds. If the EC2 instance reboots, systemd starts the service automatically.
- **ASG (min=1) provides reliability.** If the entire EC2 instance dies (hardware failure, accidental termination), the ASG automatically launches a replacement. The replacement self-configures via the bootstrap User Data script.
- **ALB provides path-based routing.** All traffic enters via the ALB. This means other services can be added behind the same ALB later — just add a new Target Group and a new listener rule with a different path prefix.
- **Secrets in SSM Parameter Store.** No secrets in code, no secrets in AMIs, no secrets in User Data scripts. All config is fetched from SSM at boot and on each deploy.

---

## AWS Resources

**Region:** `ap-southeast-2` (Sydney)
**Account:** `119312949433`

### Networking

| Resource | ID / Value | Notes |
|---|---|---|
| VPC | `vpc-093b98d73d5d4b393` | Default VPC |
| Subnet (2a) | `subnet-030d8e91d10c4c2a3` | `172.31.0.0/20`, public |
| Subnet (2b) | `subnet-08c9311e5cc3595ea` | `172.31.32.0/20`, public |
| Subnet (2c) | `subnet-0a8ec1f67f097c41e` | `172.31.16.0/20`, public |
| ALB Security Group | `sg-03eab85e05ae80cec` | `qcom-alb-sg` — inbound 80/443 from internet |
| EC2 Security Group | `sg-03d84bca0b4f0a1f7` | Existing — port 8080 inbound from ALB SG added |

### Load Balancer

| Resource | ID / Value |
|---|---|
| ALB Name | `qcom-alb` |
| ALB ARN | `arn:aws:elasticloadbalancing:ap-southeast-2:119312949433:loadbalancer/app/qcom-alb/075a14885966e8f1` |
| ALB DNS | `qcom-alb-1081983549.ap-southeast-2.elb.amazonaws.com` |
| HTTP Listener (80) | `arn:aws:elasticloadbalancing:ap-southeast-2:119312949433:listener/app/qcom-alb/075a14885966e8f1/0b092166baeace1d` — redirects all to HTTPS |
| HTTPS Listener (443) | `arn:aws:elasticloadbalancing:ap-southeast-2:119312949433:listener/app/qcom-alb/075a14885966e8f1/18c3c4076342b7cc` — path-based routing |
| Target Group | `qcom-tg` |
| Target Group ARN | `arn:aws:elasticloadbalancing:ap-southeast-2:119312949433:targetgroup/qcom-tg/b8343ad8cbe2d385` |

**Listener rules on HTTPS (443):**

| Priority | Path | Action |
|---|---|---|
| 5 | `/health` | forward → `qcom-tg` |
| 10 | `/api/v1/*` | forward → `qcom-tg` |
| default | `*` | 404 fixed response |

### Compute

| Resource | ID / Value |
|---|---|
| EC2 Instance (current) | `i-0d5e625a1593765d1` (`bunzo-main-template-wala`) |
| Instance Public IP | `13.236.184.183` |
| Instance Type | `t3.micro` |
| Launch Template | `qcom-lt` (`lt-026d20b342cbc8071`) |
| AMI used | `ami-0892a9c01908fafd1` (Amazon Linux 2, ap-southeast-2) |
| ASG Name | `qcom-asg` |
| ASG min/desired/max | `1 / 1 / 2` |

### IAM

| Resource | Value |
|---|---|
| IAM Role | `qcom-ec2-role` (`arn:aws:iam::119312949433:role/qcom-ec2-role`) |
| Inline Policy | `qcom-ssm-read` — allows `ssm:GetParameter` and `ssm:GetParametersByPath` on `arn:aws:ssm:*:*:parameter/qcom/prod/*` |
| Instance Profile | `qcom-ec2-profile` (`arn:aws:iam::119312949433:instance-profile/qcom-ec2-profile`) |

### DNS and SSL

| Resource | Value |
|---|---|
| Domain registrar | Squarespace |
| API subdomain | `api.bunzodelivery.com` |
| CNAME in Squarespace | `api` → `qcom-alb-1081983549.ap-southeast-2.elb.amazonaws.com` |
| ACM Certificate ARN | `arn:aws:acm:ap-southeast-2:119312949433:certificate/9d4c4834-fa94-4b23-b6df-91ab81e81ae3` |
| Certificate domain | `api.bunzodelivery.com` |
| Certificate status | `ISSUED` |

### SSM Parameter Store

All parameters live under `/qcom/prod/` as `SecureString` type. The EC2 instance profile has read access to all of them.

| Parameter path | Env var it maps to | Notes |
|---|---|---|
| `/qcom/prod/JWT_SECRET_KEY` | `JWT_SECRET_KEY` | Required — must be 32+ chars |
| `/qcom/prod/DYNAMODB_REGION` | `DYNAMODB_REGION` | `ap-southeast-2` (same region as EC2/ALB) |
| `/qcom/prod/DYNAMODB_TABLE_NAME` | `DYNAMODB_TABLE_NAME` | e.g. `QComTable` |
| `/qcom/prod/S3_REGION` | `S3_REGION` | e.g. `ap-southeast-2` |
| `/qcom/prod/S3_BUCKET` | `S3_BUCKET` | e.g. `printdrop-documents` |
| `/qcom/prod/GOOGLE_MAPS_API_KEY` | `GOOGLE_MAPS_API_KEY` | For geocoding |
| `/qcom/prod/PORT` | `PORT` | `8080` |
| `/qcom/prod/GITHUB_DEPLOY_KEY` | N/A — used only at bootstrap | SSH private key for cloning the repo |

**To populate these (one-time setup):**
```bash
export AWS_DEFAULT_REGION=ap-southeast-2
./scripts/setup-ssm.sh
```

**To view current values:**
```bash
aws ssm get-parameters-by-path \
  --path /qcom/prod \
  --with-decryption \
  --region ap-southeast-2 \
  --query "Parameters[*].{Name:Name,Value:Value}" \
  --output table
```

---

## Deploy Workflow

This is the day-to-day process for shipping new code.

```
1. git push origin master        ← push your changes
2. make deploy HOST=<ec2-ip> KEY=~/.ssh/your-key.pem
```

What `make deploy` does under the hood (see `scripts/deploy.sh`):
1. SSHes into the EC2 instance as `ec2-user`
2. `cd /app/qcom && git pull origin master` — pulls latest code
3. `make build` — compiles the Go binary to `bin/qcom-server`
4. `sudo bash /app/qcom/scripts/fetch-env.sh` — re-fetches all secrets from SSM and rewrites `/app/.env`
5. `sudo systemctl restart qcom` — restarts the service
6. `sudo systemctl status qcom` — prints status so you can confirm it started cleanly

**The deploy takes ~30 seconds** (mostly the Go compile).

To get the current EC2 IP at any time:
```bash
aws ec2 describe-instances \
  --filters "Name=tag:Name,Values=qcom-server" "Name=instance-state-name,Values=running" \
  --query "Reservations[0].Instances[0].PublicIpAddress" \
  --output text --region ap-southeast-2
```

---

## How a New Instance Bootstraps Itself

When the ASG launches a replacement instance (because the old one died), it runs `scripts/ec2-bootstrap.sh` as the EC2 User Data script. Here is exactly what it does, step by step:

1. **Redirects all output** to `/var/log/qcom-bootstrap.log` — if something goes wrong, this is where to look.
2. **Updates the system** (`yum update -y`) and installs `git`, `make`, `gcc`.
3. **Creates a `qcom` Linux user** with no login shell, home directory `/app`.
4. **Installs Go 1.24.3** from the official Go download CDN into `/usr/local/go/`.
5. **Fetches the GitHub deploy key** from SSM Parameter Store (`/qcom/prod/GITHUB_DEPLOY_KEY`) and writes it to `/home/qcom/.ssh/id_ed25519`.
6. **Runs `ssh-keyscan github.com`** to pre-trust GitHub's host key (avoids interactive prompt).
7. **Clones the repo** (`git@github.com:musafir-V/qcom.git`) into `/app/qcom` as the `qcom` user.
8. **Runs `make build`** to compile the binary to `/app/qcom/bin/qcom-server`.
9. **Runs `scripts/fetch-env.sh`** to fetch all SSM params and write `/app/.env`.
10. **Copies `deploy/qcom.service`** to `/etc/systemd/system/qcom.service`, enables it, and starts it.

After bootstrap completes, the instance registers itself with the ALB Target Group and starts passing health checks at `/health`. The ASG waits 120 seconds (health check grace period) before checking ELB health, giving bootstrap time to finish.

**Bootstrap takes approximately 3–5 minutes** on a t3.micro (mostly Go install + go module download).

---

## Secrets Management

Secrets flow like this:

```
Developer laptop (setup-ssm.sh)
    │
    ▼
AWS SSM Parameter Store: /qcom/prod/*  (SecureString, encrypted at rest)
    │
    ▼
EC2 Instance (via IAM Instance Profile: qcom-ec2-profile)
    │  fetch-env.sh reads all params via aws ssm get-parameters-by-path
    ▼
/app/.env  (chmod 600, owned by qcom:qcom)
    │
    ▼
systemd unit (EnvironmentFile=/app/.env)
    │
    ▼
qcom-server process (reads env vars via os.Getenv)
```

The IAM Instance Profile (`qcom-ec2-profile`) grants the EC2 instance permission to read `/qcom/prod/*` from SSM — no AWS credentials are needed on the instance itself. IAM handles auth automatically via the instance metadata service.

**To rotate a secret:**
```bash
aws ssm put-parameter \
  --name "/qcom/prod/JWT_SECRET_KEY" \
  --value "your-new-secret-value" \
  --type SecureString \
  --overwrite \
  --region ap-southeast-2
```
Then re-deploy to reload it: `make deploy HOST=<ip> KEY=<key>`

---

## Files Added to This Repo

### `deploy/qcom.service`
The systemd unit file. Installed to `/etc/systemd/system/qcom.service` on the EC2 instance by the bootstrap script. Key settings:
- Runs as `qcom` user (not root)
- Loads env from `/app/.env`
- `Restart=on-failure` with 5s delay — the process is restarted if it exits with non-zero status
- Output goes to systemd journal (view with `journalctl -u qcom`)

### `scripts/fetch-env.sh`
Runs on EC2 (as root). Queries SSM Parameter Store for all params under `/qcom/prod/`, strips the prefix from each key name, and writes them as `KEY=VALUE` lines to `/app/.env`. Sets file permissions to 600 owned by `qcom:qcom`. Called by the bootstrap script on first boot and by the deploy script on every deploy.

### `scripts/ec2-bootstrap.sh`
EC2 User Data script — runs once as root on first boot of a new instance. See the "How a New Instance Bootstraps Itself" section above for a detailed walkthrough.

### `scripts/deploy.sh`
Local deploy script run from your laptop. Accepts EC2 host and SSH key as arguments or env vars (`QCOM_EC2_HOST`, `QCOM_EC2_KEY`). Wraps the SSH commands that pull, build, and restart the service.

### `scripts/setup-ssm.sh`
One-time interactive script to populate SSM Parameter Store. Prompts for each secret value and creates/updates the corresponding SSM SecureString parameter. Run this once when setting up a new environment or when rotating secrets in bulk.

---

## Step-by-Step: What Was Built and Why

### Why this architecture was chosen

The original workflow was: push to Git → SSH into EC2 → `git pull` → `go build` → run server manually. This broke whenever the EC2 instance restarted because the server wasn't managed by anything — it just died.

The goal was to automate that workflow while adding:
- **Reliability:** instance auto-replaces itself if it dies
- **HTTPS:** proper SSL via ACM, not self-signed
- **Path-based routing:** so other services can be added behind the same domain later
- **Secret management:** no more env vars hardcoded anywhere

We deliberately kept it simple: no Docker, no Kubernetes, no CI/CD pipeline. The Go binary is compiled on the instance itself, which means the instance is always self-sufficient. If ECR goes down or Docker has issues, nothing breaks.

### What was created, in order

**1. systemd unit file** (`deploy/qcom.service`)
Created to manage the `qcom-server` process. Before this, the process ran in a `screen` or `nohup` session and didn't restart on crash or reboot.

**2. SSM env-fetch script** (`scripts/fetch-env.sh`)
Created to load secrets at runtime from SSM rather than hardcoding them. This script runs at boot and on every deploy, ensuring the running service always has the latest secret values.

**3. EC2 bootstrap script** (`scripts/ec2-bootstrap.sh`)
Created as the EC2 User Data script for the Launch Template. This is what makes new ASG instances self-configuring — they don't need any manual setup. The script is stored in the repo (not in the Launch Template console) so it's version-controlled and reviewable.

**4. Deploy script** (`scripts/deploy.sh`)
Created to formalize the deploy process into a single command. Before this, deploys required manually remembering 4 SSH commands in the right order.

**5. Makefile `deploy` target**
Wraps `scripts/deploy.sh` so deploys are as simple as `make deploy HOST=x KEY=y`.

**6. SSM setup script** (`scripts/setup-ssm.sh`)
Created for initial secret population and bulk rotation. Interactive — prompts for each value rather than requiring you to remember all the param names.

**7. IAM Instance Profile**
Created so EC2 instances can read SSM parameters without any AWS credentials stored on the instance. The role (`qcom-ec2-role`) has a minimal policy — only `ssm:GetParameter` and `ssm:GetParametersByPath` on `/qcom/prod/*`. Nothing else.

**8. ALB + Target Group + Listener Rules**
Created the full ALB stack from scratch (no ALB existed before). Steps:
- Created `qcom-alb-sg` security group: inbound 80/443 from internet
- Added inbound rule to the EC2 security group: port 8080 from `qcom-alb-sg`
- Created internet-facing ALB `qcom-alb` across all 3 AZs (2a, 2b, 2c)
- Created Target Group `qcom-tg`: HTTP/8080, health check on `/health`
- Registered instance `i-0d5e625a1593765d1` with the target group
- Created HTTP listener (80): redirects all traffic to HTTPS 301
- Created HTTPS listener (443): path rules for `/health` and `/api/v1/*`

**9. ACM Certificate + DNS**
- Requested cert for `api.bunzodelivery.com` via ACM with DNS validation
- Added validation CNAME (`_24c331ce017ce104c586254705a68d52.api`) in Squarespace DNS
- Added API subdomain CNAME (`api` → ALB DNS name) in Squarespace DNS
- Certificate issued and attached to HTTPS listener

**10. Launch Template + ASG**
- Created Launch Template `qcom-lt` with the bootstrap script as User Data and `qcom-ec2-profile` as the IAM Instance Profile
- Created ASG `qcom-asg` (min=1, desired=1, max=2) linked to the target group
- The ASG uses ELB health checks — if the ALB health check fails, the ASG replaces the instance

---

## Troubleshooting Runbook

### Service is down — `https://api.bunzodelivery.com/health` returns error

**Step 1: Check ALB target health**
```bash
aws elbv2 describe-target-health \
  --target-group-arn arn:aws:elasticloadbalancing:ap-southeast-2:119312949433:targetgroup/qcom-tg/b8343ad8cbe2d385 \
  --query "TargetHealthDescriptions[*].{Id:Target.Id,State:TargetHealth.State,Reason:TargetHealth.Reason}" \
  --output table --region ap-southeast-2
```

- If state is `healthy` → the service is up, the issue is elsewhere (DNS, cert)
- If state is `unhealthy` → go to Step 2
- If state is `unused` or no targets → the instance was deregistered or the ASG hasn't launched yet

**Step 2: Check if the instance is running**
```bash
aws ec2 describe-instances \
  --filters "Name=tag:aws:autoscaling:groupName,Values=qcom-asg" \
  --query "Reservations[*].Instances[*].{ID:InstanceId,State:State.Name,IP:PublicIpAddress}" \
  --output table --region ap-southeast-2
```

- If no instances → ASG hasn't launched yet (wait a minute and re-check)
- If instance is `running` → SSH in and check the service

**Step 3: SSH in and check systemd**
```bash
ssh -i ~/.ssh/your-key.pem ec2-user@<instance-ip>
sudo systemctl status qcom
sudo journalctl -u qcom -n 100 --no-pager
```

Common issues:
- `code=exited, status=1` → the binary crashed on startup. Check logs for the error. Most likely a missing env var or failed DynamoDB connection.
- `code=exited, status=203/EXEC` → the binary file doesn't exist at `/app/qcom/bin/qcom-server`. Run the bootstrap manually or re-deploy.

**Step 4: Check /app/.env exists and has content**
```bash
sudo cat /app/.env
```
If empty or missing → run `sudo bash /app/qcom/scripts/fetch-env.sh` to re-fetch from SSM.

---

### New instance launched by ASG but bootstrap failed

Check the bootstrap log:
```bash
ssh -i ~/.ssh/your-key.pem ec2-user@<new-instance-ip>
sudo cat /var/log/qcom-bootstrap.log
```

Common failures:
- **SSM permission denied** → the instance profile wasn't attached. Check the Launch Template has `qcom-ec2-profile` set. Verify: `curl -s http://169.254.169.254/latest/meta-data/iam/info`
- **git clone failed** → the deploy key in SSM (`/qcom/prod/GITHUB_DEPLOY_KEY`) is wrong or missing. Re-run `setup-ssm.sh` to update it.
- **make build failed** → compilation error in the code. Check the log for the Go error. Fix the code and re-deploy to the existing instance.
- **Go not found** → Go install failed. Check `/usr/local/go/bin/go` exists. If not, re-run the Go install step manually.

---

### Deploy fails mid-way

If `make deploy` fails partway through, the service may be in an inconsistent state. SSH in manually and run the remaining steps:
```bash
ssh -i ~/.ssh/your-key.pem ec2-user@<ip>
cd /app/qcom
git pull origin master                            # if not done
PATH=$PATH:/usr/local/go/bin make build           # if not done
sudo bash /app/qcom/scripts/fetch-env.sh          # if not done
sudo systemctl restart qcom                       # restart regardless
sudo systemctl status qcom                        # verify
```

---

### SSL certificate expired or errors

Check cert expiry:
```bash
aws acm describe-certificate \
  --certificate-arn arn:aws:acm:ap-southeast-2:119312949433:certificate/9d4c4834-fa94-4b23-b6df-91ab81e81ae3 \
  --region ap-southeast-2 \
  --query "Certificate.{Status:Status,Expiry:NotAfter,RenewalStatus:RenewalSummary.RenewalStatus}" \
  --output table
```

ACM auto-renews certificates as long as the DNS CNAME validation record (`_24c331ce017ce104c586254705a68d52.api` in Squarespace) is still present. **Do not delete that CNAME record.**

---

### ALB returns 404 for all requests

Check listener rules:
```bash
aws elbv2 describe-rules \
  --listener-arn arn:aws:elasticloadbalancing:ap-southeast-2:119312949433:listener/app/qcom-alb/075a14885966e8f1/18c3c4076342b7cc \
  --query "Rules[*].{Priority:Priority,Path:Conditions[0].Values[0],Action:Actions[0].Type}" \
  --output table --region ap-southeast-2
```

Expected output: priority 5 → `/health` forward, priority 10 → `/api/v1/*` forward, default → fixed-response 404.

If the `/api/v1/*` rule is missing, re-add it:
```bash
aws elbv2 create-rule \
  --listener-arn arn:aws:elasticloadbalancing:ap-southeast-2:119312949433:listener/app/qcom-alb/075a14885966e8f1/18c3c4076342b7cc \
  --priority 10 \
  --conditions '[{"Field":"path-pattern","Values":["/api/v1/*"]}]' \
  --actions '[{"Type":"forward","TargetGroupArn":"arn:aws:elasticloadbalancing:ap-southeast-2:119312949433:targetgroup/qcom-tg/b8343ad8cbe2d385"}]' \
  --region ap-southeast-2
```

---

### Adding a new service behind the same ALB

To add a second service (e.g. `orders-service`) to the same ALB:

1. Create a new Target Group for the service
2. Register its EC2 instance(s) with the new target group
3. Add a listener rule with the next priority (e.g. 20) and a different path prefix (e.g. `/api/v2/*`)

```bash
NEW_TG=$(aws elbv2 create-target-group \
  --name orders-tg \
  --protocol HTTP --port 8080 \
  --vpc-id vpc-093b98d73d5d4b393 \
  --health-check-path /health \
  --region ap-southeast-2 \
  --query "TargetGroups[0].TargetGroupArn" --output text)

aws elbv2 create-rule \
  --listener-arn arn:aws:elasticloadbalancing:ap-southeast-2:119312949433:listener/app/qcom-alb/075a14885966e8f1/18c3c4076342b7cc \
  --priority 20 \
  --conditions '[{"Field":"path-pattern","Values":["/api/orders/*"]}]' \
  --actions "[{\"Type\":\"forward\",\"TargetGroupArn\":\"${NEW_TG}\"}]" \
  --region ap-southeast-2
```

---

## How to Rebuild from Scratch

If you ever need to tear down and rebuild the entire infrastructure:

### 1. Recreate IAM role and instance profile
```bash
# Trust policy
cat > /tmp/ec2-trust-policy.json << 'EOF'
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}
EOF

aws iam create-role --role-name qcom-ec2-role \
  --assume-role-policy-document file:///tmp/ec2-trust-policy.json

aws iam put-role-policy --role-name qcom-ec2-role \
  --policy-name qcom-ssm-read \
  --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["ssm:GetParameter","ssm:GetParametersByPath"],"Resource":"arn:aws:ssm:*:*:parameter/qcom/prod/*"}]}'

aws iam create-instance-profile --instance-profile-name qcom-ec2-profile
aws iam add-role-to-instance-profile \
  --instance-profile-name qcom-ec2-profile --role-name qcom-ec2-role
```

### 2. Populate SSM parameters
```bash
export AWS_DEFAULT_REGION=ap-southeast-2
./scripts/setup-ssm.sh
```

### 3. Create ALB security group
```bash
ALB_SG=$(aws ec2 create-security-group \
  --group-name qcom-alb-sg \
  --description "ALB SG for qcom" \
  --vpc-id vpc-093b98d73d5d4b393 \
  --query "GroupId" --output text --region ap-southeast-2)

aws ec2 authorize-security-group-ingress --group-id "${ALB_SG}" \
  --protocol tcp --port 80 --cidr 0.0.0.0/0 --region ap-southeast-2
aws ec2 authorize-security-group-ingress --group-id "${ALB_SG}" \
  --protocol tcp --port 443 --cidr 0.0.0.0/0 --region ap-southeast-2

# Allow ALB to reach EC2 on 8080
aws ec2 authorize-security-group-ingress \
  --group-id <ec2-security-group-id> \
  --protocol tcp --port 8080 \
  --source-group "${ALB_SG}" --region ap-southeast-2
```

### 4. Create ALB, Target Group, Listeners
```bash
ALB_ARN=$(aws elbv2 create-load-balancer \
  --name qcom-alb \
  --subnets subnet-030d8e91d10c4c2a3 subnet-08c9311e5cc3595ea subnet-0a8ec1f67f097c41e \
  --security-groups "${ALB_SG}" \
  --scheme internet-facing --type application \
  --query "LoadBalancers[0].LoadBalancerArn" --output text --region ap-southeast-2)

TG_ARN=$(aws elbv2 create-target-group \
  --name qcom-tg --protocol HTTP --port 8080 \
  --vpc-id vpc-093b98d73d5d4b393 \
  --health-check-path /health \
  --health-check-interval-seconds 15 \
  --healthy-threshold-count 2 --unhealthy-threshold-count 3 \
  --query "TargetGroups[0].TargetGroupArn" --output text --region ap-southeast-2)

# HTTP listener (redirect to HTTPS)
aws elbv2 create-listener \
  --load-balancer-arn "${ALB_ARN}" --protocol HTTP --port 80 \
  --default-actions '[{"Type":"redirect","RedirectConfig":{"Protocol":"HTTPS","Port":"443","StatusCode":"HTTP_301"}}]' \
  --region ap-southeast-2

# ACM cert (you must add the DNS validation CNAME in Squarespace before proceeding)
CERT_ARN=$(aws acm request-certificate \
  --domain-name api.bunzodelivery.com \
  --validation-method DNS \
  --query "CertificateArn" --output text --region ap-southeast-2)

# Wait for cert to be ISSUED, then create HTTPS listener
HTTPS_ARN=$(aws elbv2 create-listener \
  --load-balancer-arn "${ALB_ARN}" --protocol HTTPS --port 443 \
  --certificates "CertificateArn=${CERT_ARN}" \
  --default-actions '[{"Type":"fixed-response","FixedResponseConfig":{"StatusCode":"404","ContentType":"text/plain","MessageBody":"Not found"}}]' \
  --query "Listeners[0].ListenerArn" --output text --region ap-southeast-2)

aws elbv2 create-rule --listener-arn "${HTTPS_ARN}" --priority 5 \
  --conditions '[{"Field":"path-pattern","Values":["/health"]}]' \
  --actions "[{\"Type\":\"forward\",\"TargetGroupArn\":\"${TG_ARN}\"}]" --region ap-southeast-2

aws elbv2 create-rule --listener-arn "${HTTPS_ARN}" --priority 10 \
  --conditions '[{"Field":"path-pattern","Values":["/api/v1/*"]}]' \
  --actions "[{\"Type\":\"forward\",\"TargetGroupArn\":\"${TG_ARN}\"}]" --region ap-southeast-2
```

### 5. Create Launch Template and ASG
```bash
USERDATA=$(base64 -i scripts/ec2-bootstrap.sh)

LT_ID=$(aws ec2 create-launch-template \
  --launch-template-name qcom-lt \
  --launch-template-data "{
    \"ImageId\": \"ami-0892a9c01908fafd1\",
    \"InstanceType\": \"t3.micro\",
    \"UserData\": \"${USERDATA}\",
    \"IamInstanceProfile\": {\"Name\": \"qcom-ec2-profile\"},
    \"SecurityGroupIds\": [\"<ec2-security-group-id>\"],
    \"TagSpecifications\": [{\"ResourceType\":\"instance\",\"Tags\":[{\"Key\":\"Name\",\"Value\":\"qcom-server\"}]}]
  }" \
  --query "LaunchTemplate.LaunchTemplateId" --output text --region ap-southeast-2)

aws autoscaling create-auto-scaling-group \
  --auto-scaling-group-name qcom-asg \
  --launch-template "LaunchTemplateId=${LT_ID},Version=1" \
  --min-size 1 --desired-capacity 1 --max-size 2 \
  --vpc-zone-identifier "subnet-030d8e91d10c4c2a3,subnet-08c9311e5cc3595ea,subnet-0a8ec1f67f097c41e" \
  --target-group-arns "${TG_ARN}" \
  --health-check-type ELB --health-check-grace-period 120 \
  --region ap-southeast-2
```

### 6. Update Squarespace DNS
Add these two CNAMEs in Squarespace → Domains → bunzodelivery.com → DNS Settings:
- `api` → `<new-alb-dns-name>.ap-southeast-2.elb.amazonaws.com`
- ACM validation CNAME (get from `aws acm describe-certificate --certificate-arn <arn>`)
