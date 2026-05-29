# Handoff: Add product-service to the ALB

## What this session needs to do

There is already a running Java service on EC2 (`product-service`). The goal is to:
1. Register it with the existing ALB (`qcom-alb`) behind a new Target Group
2. Add path-based listener rules on the HTTPS listener so traffic is routed based on URL path
3. Set up systemd on the instance so the Java service runs reliably and restarts on crash/reboot
4. (Optional) Add an SSM-based secrets fetch if the Java service has secrets

This is exactly what was already done for the Go `qcom` service in the same AWS account. Everything below gives you all the IDs and context you need.

---

## Existing infrastructure (already built — do not recreate)

**AWS Account:** `119312949433`
**Region:** `ap-southeast-2` (Sydney)

### VPC / Networking
| Resource | ID |
|---|---|
| VPC | `vpc-093b98d73d5d4b393` |
| Subnet ap-southeast-2a | `subnet-030d8e91d10c4c2a3` |
| Subnet ap-southeast-2b | `subnet-08c9311e5cc3595ea` |
| Subnet ap-southeast-2c | `subnet-0a8ec1f67f097c41e` |
| ALB Security Group | `sg-03eab85e05ae80cec` (`qcom-alb-sg`) — inbound 80/443 from internet |

### ALB
| Resource | Value |
|---|---|
| ALB Name | `qcom-alb` |
| ALB ARN | `arn:aws:elasticloadbalancing:ap-southeast-2:119312949433:loadbalancer/app/qcom-alb/075a14885966e8f1` |
| ALB DNS | `qcom-alb-1081983549.ap-southeast-2.elb.amazonaws.com` |
| HTTP Listener (80) | redirects all traffic to HTTPS 301 — do not touch |
| HTTPS Listener (443) ARN | `arn:aws:elasticloadbalancing:ap-southeast-2:119312949433:listener/app/qcom-alb/075a14885966e8f1/18c3c4076342b7cc` |

**Existing HTTPS listener rules:**
| Priority | Path | Routes to |
|---|---|---|
| 5 | `/health` | `qcom-tg` (Go service) |
| 10 | `/api/v1/*` | `qcom-tg` (Go service) |
| default | everything else | 404 |

### DNS
- Domain: `bunzodelivery.com` (registered on Squarespace)
- `api.bunzodelivery.com` → ALB DNS name (CNAME in Squarespace)
- ACM cert for `api.bunzodelivery.com`: `arn:aws:acm:ap-southeast-2:119312949433:certificate/9d4c4834-fa94-4b23-b6df-91ab81e81ae3` — already attached to the HTTPS listener, already ISSUED

---

## The product-service EC2 instance

| Property | Value |
|---|---|
| Instance ID | `i-00dc197caba8ab3eb` |
| Public IP | `54.253.97.113` |
| Instance Name | `product-service` |
| Instance Type | `t3.medium` |
| Availability Zone | `ap-southeast-2c` |
| Subnet | `subnet-0a8ec1f67f097c41e` |
| Security Group | `sg-020131b59c1923a93` |
| SSH Key Pair | unknown — check with user |

**This is a Java service.** Find out:
- What port it listens on (likely 8080 or 8443 — check with user or inspect the running process with `ss -tlnp`)
- What path prefix it should own (e.g. `/api/products/*` or `/api/orders/*`) — ask the user
- How it is currently started (systemd? screen? nohup? startup script?) — check with `systemctl list-units --type=service --state=running`
- Whether it needs secrets — check with user

---

## What to do, in order

### Step 1: Find out what port the Java service runs on

SSH in and check:
```bash
ssh -i ~/.ssh/<key>.pem ec2-user@54.253.97.113
ss -tlnp | grep java
# or
sudo netstat -tlnp | grep java
```

### Step 2: Allow ALB to reach the Java service port

Add an inbound rule to the product-service security group (`sg-020131b59c1923a93`) allowing traffic from the ALB SG (`sg-03eab85e05ae80cec`) on whatever port Java is using:

```bash
aws ec2 authorize-security-group-ingress \
  --group-id sg-020131b59c1923a93 \
  --protocol tcp \
  --port <java-port> \
  --source-group sg-03eab85e05ae80cec \
  --region ap-southeast-2
```

### Step 3: Create a Target Group for product-service

Ask the user what health check endpoint the Java service exposes (e.g. `/health`, `/actuator/health`, `/ping`).

```bash
PRODUCT_TG=$(aws elbv2 create-target-group \
  --name product-tg \
  --protocol HTTP \
  --port <java-port> \
  --vpc-id vpc-093b98d73d5d4b393 \
  --target-type instance \
  --health-check-protocol HTTP \
  --health-check-path <health-endpoint> \
  --health-check-interval-seconds 15 \
  --health-check-timeout-seconds 5 \
  --healthy-threshold-count 2 \
  --unhealthy-threshold-count 3 \
  --query "TargetGroups[0].TargetGroupArn" --output text \
  --region ap-southeast-2)
echo "Product TG: ${PRODUCT_TG}"
```

### Step 4: Register the EC2 instance with the target group

```bash
aws elbv2 register-targets \
  --target-group-arn "${PRODUCT_TG}" \
  --targets Id=i-00dc197caba8ab3eb \
  --region ap-southeast-2
```

### Step 5: Add listener rule on the HTTPS listener

Pick a priority higher than 10 (already used by qcom). Use 20.
Ask the user what path prefix product-service should own (e.g. `/api/products/*`).

```bash
HTTPS_LISTENER="arn:aws:elasticloadbalancing:ap-southeast-2:119312949433:listener/app/qcom-alb/075a14885966e8f1/18c3c4076342b7cc"

aws elbv2 create-rule \
  --listener-arn "${HTTPS_LISTENER}" \
  --priority 20 \
  --conditions '[{"Field":"path-pattern","Values":["<path-prefix>"]}]' \
  --actions "[{\"Type\":\"forward\",\"TargetGroupArn\":\"${PRODUCT_TG}\"}]" \
  --region ap-southeast-2
```

### Step 6: Set up systemd for the Java service

SSH in and check how Java is currently being run:
```bash
ssh -i ~/.ssh/<key>.pem ec2-user@54.253.97.113
sudo systemctl list-units --type=service --state=running
ps aux | grep java
```

If it's running via `screen` / `nohup` / a startup script and not systemd, create a systemd unit. A typical Java service unit:

```ini
# /etc/systemd/system/product-service.service
[Unit]
Description=product-service
After=network.target

[Service]
Type=simple
User=<service-user>
WorkingDirectory=<app-directory>
EnvironmentFile=/app/.env
ExecStart=/usr/bin/java -jar <path-to-jar>
Restart=on-failure
RestartSec=5s
StandardOutput=journal
StandardError=journal
SyslogIdentifier=product-service

[Install]
WantedBy=multi-user.target
```

Enable and start:
```bash
sudo systemctl daemon-reload
sudo systemctl enable product-service
sudo systemctl start product-service
```

### Step 7: Verify target health

```bash
aws elbv2 describe-target-health \
  --target-group-arn "${PRODUCT_TG}" \
  --query "TargetHealthDescriptions[*].{Id:Target.Id,State:TargetHealth.State,Reason:TargetHealth.Reason}" \
  --output table --region ap-southeast-2
```

Expected: state = `healthy`. If `unhealthy`, check the reason field — usually wrong port or health check path returning non-200.

### Step 8: Smoke test

```bash
curl -s https://api.bunzodelivery.com<path-prefix>/some-endpoint
```

---

## Key things to ask the user before starting

1. What port does the Java service listen on?
2. What path prefix should it own? (e.g. `/api/products/*`)
3. Does it have a health check endpoint? If yes, what path?
4. What SSH key pair is on the `product-service` instance?
5. Does it need secrets from SSM, or does it manage its own config?
6. Is it a Spring Boot app? (If yes, `/actuator/health` is the standard health endpoint)

---

## Reference: how the qcom (Go) service was set up

The identical exercise was done for `qcom` in the same session. For reference:
- Target Group: `qcom-tg` (`arn:aws:elasticloadbalancing:ap-southeast-2:119312949433:targetgroup/qcom-tg/b8343ad8cbe2d385`)
- Listener rule priority 10 → `/api/v1/*`
- Health check path: `/health`
- Service port: `8080`
- Instance: `i-0d5e625a1593765d1`
- systemd unit: `deploy/qcom.service` in this repo — use as a reference template for the Java unit
