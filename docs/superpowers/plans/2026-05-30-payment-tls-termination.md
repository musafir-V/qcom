# Payment TLS Termination Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire up HTTPS termination at `payment.bunzodelivery.com` in front of EC2 `i-00dc197caba8ab3eb:8080` (product-service) by adding two shell scripts under `scripts/` that drive the AWS CLI to create an ACM certificate, security groups, target group, ALB, and listeners.

**Architecture:** Two new files only. `scripts/payment-tls-lib.sh` holds sourced helper functions. `scripts/setup-payment-tls.sh` is the orchestrator with subcommands. Every resource is named `payment-*`, every create operation is idempotent. No existing files change.

**Tech Stack:** Bash, `aws` CLI v2 (`acm`, `ec2`, `elbv2`), `jq` for JSON parsing.

**Spec:** [docs/superpowers/specs/2026-05-30-payment-tls-termination-design.md](../specs/2026-05-30-payment-tls-termination-design.md)

---

## File Structure

- **Create:** `scripts/payment-tls-lib.sh` — sourced helpers: logging, command checks, AWS lookups (VPC for instance, public subnets, cert ARN by domain, SG/TG/ALB ARNs by name).
- **Create:** `scripts/setup-payment-tls.sh` — orchestrator. Top-of-file config (env-overridable), subcommand dispatch, one bash function per subcommand. Sources `payment-tls-lib.sh`.

No other files touched.

---

## Task 1: Library skeleton with logging and command checks

**Files:**
- Create: `scripts/payment-tls-lib.sh`

- [ ] **Step 1: Write the library skeleton**

```bash
#!/usr/bin/env bash
# Sourced helpers for setup-payment-tls.sh. Do not execute directly.

log() {
  printf '[%s] %s\n' "$(date '+%H:%M:%S')" "$*" >&2
}

die() {
  log "ERROR: $*"
  exit 1
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

require_tools() {
  require_cmd aws
  require_cmd jq
}
```

- [ ] **Step 2: Verify syntax**

Run: `bash -n scripts/payment-tls-lib.sh`
Expected: exit 0, no output.

- [ ] **Step 3: Smoke-test the helpers**

Run: `bash -c 'source scripts/payment-tls-lib.sh && log hello && require_tools && echo ok'`
Expected: `[HH:MM:SS] hello` on stderr, then `ok` on stdout. If `aws` or `jq` isn't installed, the message names the missing binary and exits non-zero.

- [ ] **Step 4: Commit**

```bash
git add scripts/payment-tls-lib.sh
git commit -m "feat(infra): payment-tls-lib helpers (log, die, require_cmd)"
```

---

## Task 2: AWS lookup helpers (VPC + subnets for the instance)

**Files:**
- Modify: `scripts/payment-tls-lib.sh` (append)

- [ ] **Step 1: Add the VPC / subnet helpers**

Append to `scripts/payment-tls-lib.sh`:

```bash
vpc_of_instance() {
  local instance_id="$1" region="$2"
  aws ec2 describe-instances \
    --region "$region" \
    --instance-ids "$instance_id" \
    --query 'Reservations[0].Instances[0].VpcId' \
    --output text
}

# Print up to N public subnet IDs (subnets whose route table has an IGW route)
# from the given VPC, choosing one subnet per AZ. Args: vpc_id region [max=2]
public_subnets_in_vpc() {
  local vpc_id="$1" region="$2" max="${3:-2}"

  # All subnets in the VPC, with their AZ.
  local subnets_json
  subnets_json=$(aws ec2 describe-subnets \
    --region "$region" \
    --filters "Name=vpc-id,Values=$vpc_id" \
    --query 'Subnets[].{Id:SubnetId,Az:AvailabilityZone}' \
    --output json)

  # All route tables in the VPC plus the main route table (used for any
  # subnet without an explicit association).
  local rts_json
  rts_json=$(aws ec2 describe-route-tables \
    --region "$region" \
    --filters "Name=vpc-id,Values=$vpc_id" \
    --output json)

  # For each subnet, determine the effective route table, then check whether
  # any route in it targets an internet gateway (igw-*). One per AZ, up to max.
  jq -r --argjson rts "$rts_json" --argjson n "$max" '
    . as $subnets
    | [ $subnets[] | . + {
        public: (
          ($rts.RouteTables[] | select(.Associations[]?.SubnetId == .Id))
          // ($rts.RouteTables[] | select(.Associations[]?.Main == true))
          | .Routes // []
          | any(.GatewayId? // "" | startswith("igw-"))
        )
      } ]
    | map(select(.public == true))
    | group_by(.Az) | map(.[0])
    | .[:$n]
    | .[].Id
  ' <<<"$subnets_json"
}
```

- [ ] **Step 2: Verify syntax**

Run: `bash -n scripts/payment-tls-lib.sh`
Expected: exit 0.

- [ ] **Step 3: Manual sanity check against real AWS (live)**

Run (requires AWS credentials for the account that owns the instance):

```bash
source scripts/payment-tls-lib.sh
vpc=$(vpc_of_instance i-00dc197caba8ab3eb ap-southeast-2)
echo "VPC: $vpc"
public_subnets_in_vpc "$vpc" ap-southeast-2 2
```

Expected: prints a non-empty `vpc-xxxx`, then 1 or 2 `subnet-xxxx` IDs on separate lines. If fewer than 2 subnets, that is OK to surface — it tells us there is no second-AZ public subnet (a precondition the orchestrator must check).

- [ ] **Step 4: Commit**

```bash
git add scripts/payment-tls-lib.sh
git commit -m "feat(infra): payment-tls-lib VPC + public subnet lookups"
```

---

## Task 3: AWS lookup helpers (cert / SG / TG / ALB by name)

**Files:**
- Modify: `scripts/payment-tls-lib.sh` (append)

- [ ] **Step 1: Add the lookup helpers**

Append to `scripts/payment-tls-lib.sh`:

```bash
# Print the ARN of the most recently issued or pending ACM cert for the given
# domain in the given region, or empty string if none.
cert_arn_for_domain() {
  local domain="$1" region="$2"
  aws acm list-certificates \
    --region "$region" \
    --certificate-statuses PENDING_VALIDATION ISSUED \
    --query "CertificateSummaryList[?DomainName=='$domain'].CertificateArn | [0]" \
    --output text | sed 's/^None$//'
}

sg_id_by_name() {
  local name="$1" vpc_id="$2" region="$3"
  aws ec2 describe-security-groups \
    --region "$region" \
    --filters "Name=group-name,Values=$name" "Name=vpc-id,Values=$vpc_id" \
    --query 'SecurityGroups[0].GroupId' \
    --output text | sed 's/^None$//'
}

tg_arn_by_name() {
  local name="$1" region="$2"
  aws elbv2 describe-target-groups \
    --region "$region" \
    --names "$name" \
    --query 'TargetGroups[0].TargetGroupArn' \
    --output text 2>/dev/null | sed 's/^None$//' || true
}

alb_arn_by_name() {
  local name="$1" region="$2"
  aws elbv2 describe-load-balancers \
    --region "$region" \
    --names "$name" \
    --query 'LoadBalancers[0].LoadBalancerArn' \
    --output text 2>/dev/null | sed 's/^None$//' || true
}

alb_dns_by_arn() {
  local arn="$1" region="$2"
  aws elbv2 describe-load-balancers \
    --region "$region" \
    --load-balancer-arns "$arn" \
    --query 'LoadBalancers[0].DNSName' \
    --output text
}
```

- [ ] **Step 2: Verify syntax**

Run: `bash -n scripts/payment-tls-lib.sh`
Expected: exit 0.

- [ ] **Step 3: Live sanity check**

Run:

```bash
source scripts/payment-tls-lib.sh
echo "cert: '$(cert_arn_for_domain payment.bunzodelivery.com ap-southeast-2)'"
echo "tg:   '$(tg_arn_by_name payment-tg ap-southeast-2)'"
echo "alb:  '$(alb_arn_by_name payment-alb ap-southeast-2)'"
```

Expected on a fresh account: all three print empty strings between the quotes (no error). Confirms "not found" returns empty, not `None`.

- [ ] **Step 4: Commit**

```bash
git add scripts/payment-tls-lib.sh
git commit -m "feat(infra): payment-tls-lib cert/SG/TG/ALB lookups by name"
```

---

## Task 4: Orchestrator skeleton (config + dispatch + usage)

**Files:**
- Create: `scripts/setup-payment-tls.sh`

- [ ] **Step 1: Write the skeleton**

```bash
#!/usr/bin/env bash
# Set up TLS termination at payment.bunzodelivery.com in front of an EC2 host.
# See docs/superpowers/specs/2026-05-30-payment-tls-termination-design.md
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=payment-tls-lib.sh
source "${SCRIPT_DIR}/payment-tls-lib.sh"

# --- Config (override via env) -----------------------------------------------
REGION="${AWS_REGION:-ap-southeast-2}"
INSTANCE_ID="${INSTANCE_ID:-i-00dc197caba8ab3eb}"
DOMAIN="${DOMAIN:-payment.bunzodelivery.com}"
TARGET_PORT="${TARGET_PORT:-8080}"
HEALTH_PATH="${HEALTH_PATH:-/actuator/health}"
NAME_PREFIX="${NAME_PREFIX:-payment}"
WAIT_CERT_TIMEOUT_SECONDS="${WAIT_CERT_TIMEOUT_SECONDS:-1800}"

ALB_SG_NAME="${NAME_PREFIX}-alb-sg"
TG_NAME="${NAME_PREFIX}-tg"
ALB_NAME="${NAME_PREFIX}-alb"
SSL_POLICY="ELBSecurityPolicy-TLS13-1-2-2021-06"

usage() {
  cat <<EOF
Usage: $0 <subcommand>

Subcommands:
  request-cert      Request ACM cert for \$DOMAIN, print DNS validation record.
  wait-cert         Poll ACM until cert is ISSUED.
  security-groups   Create ALB SG and add :\${TARGET_PORT} ingress to the EC2 SG.
  target-group      Create target group and register the EC2 instance.
  alb               Create the Application Load Balancer.
  listeners         Create HTTPS:443 and HTTP:80 listeners.
  status            Print cert, ALB DNS, target health.
  all               Run all subcommands in order (blocks on cert validation).

Config (overridable via env):
  AWS_REGION=$REGION
  INSTANCE_ID=$INSTANCE_ID
  DOMAIN=$DOMAIN
  TARGET_PORT=$TARGET_PORT
  HEALTH_PATH=$HEALTH_PATH
  NAME_PREFIX=$NAME_PREFIX
EOF
}

main() {
  require_tools
  local cmd="${1:-}"
  case "$cmd" in
    request-cert|wait-cert|security-groups|target-group|alb|listeners|status|all)
      "cmd_${cmd//-/_}"
      ;;
    ""|-h|--help)
      usage
      ;;
    *)
      usage
      die "unknown subcommand: $cmd"
      ;;
  esac
}

# Subcommand stubs — filled in by later tasks.
cmd_request_cert()    { die "not implemented yet (Task 5)"; }
cmd_wait_cert()       { die "not implemented yet (Task 6)"; }
cmd_security_groups() { die "not implemented yet (Task 7)"; }
cmd_target_group()    { die "not implemented yet (Task 8)"; }
cmd_alb()             { die "not implemented yet (Task 9)"; }
cmd_listeners()       { die "not implemented yet (Task 10)"; }
cmd_status()          { die "not implemented yet (Task 11)"; }
cmd_all()             { die "not implemented yet (Task 12)"; }

main "$@"
```

- [ ] **Step 2: Make executable and verify syntax**

Run:
```bash
chmod +x scripts/setup-payment-tls.sh
bash -n scripts/setup-payment-tls.sh
```
Expected: exit 0, no output.

- [ ] **Step 3: Verify dispatch**

Run: `./scripts/setup-payment-tls.sh`
Expected: prints the usage block, exits 0.

Run: `./scripts/setup-payment-tls.sh bogus`
Expected: prints usage, then `[..] ERROR: unknown subcommand: bogus`, exits non-zero.

Run: `./scripts/setup-payment-tls.sh request-cert`
Expected: `[..] ERROR: not implemented yet (Task 5)`, exits non-zero.

- [ ] **Step 4: Commit**

```bash
git add scripts/setup-payment-tls.sh
git commit -m "feat(infra): setup-payment-tls.sh skeleton with subcommand dispatch"
```

---

## Task 5: `request-cert` subcommand

**Files:**
- Modify: `scripts/setup-payment-tls.sh` — replace the `cmd_request_cert` stub.

- [ ] **Step 1: Replace the stub**

In `scripts/setup-payment-tls.sh`, replace:

```bash
cmd_request_cert()    { die "not implemented yet (Task 5)"; }
```

With:

```bash
cmd_request_cert() {
  local arn
  arn=$(cert_arn_for_domain "$DOMAIN" "$REGION")

  if [[ -z "$arn" ]]; then
    log "Requesting ACM cert for $DOMAIN in $REGION..."
    arn=$(aws acm request-certificate \
      --region "$REGION" \
      --domain-name "$DOMAIN" \
      --validation-method DNS \
      --tags Key=Name,Value="${NAME_PREFIX}-cert" \
      --query CertificateArn --output text)
    log "Created: $arn"
    # Give ACM a moment to populate ResourceRecord.
    sleep 5
  else
    log "Cert already exists: $arn"
  fi

  local name value type
  read -r name type value < <(aws acm describe-certificate \
    --region "$REGION" \
    --certificate-arn "$arn" \
    --query 'Certificate.DomainValidationOptions[0].ResourceRecord.[Name,Type,Value]' \
    --output text)

  if [[ "$name" == "None" || -z "$name" ]]; then
    die "ACM has not yet published the validation record. Re-run request-cert in a few seconds."
  fi

  cat <<EOF

==== ACM DNS validation ====
Add this CNAME at your DNS provider, then run: $0 wait-cert

  Name:  $name
  Type:  $type
  Value: $value

Cert ARN: $arn
EOF
}
```

- [ ] **Step 2: Verify syntax**

Run: `bash -n scripts/setup-payment-tls.sh`
Expected: exit 0.

- [ ] **Step 3: Live run**

Run: `./scripts/setup-payment-tls.sh request-cert`
Expected: prints `Requesting ACM cert ...` (first run) or `Cert already exists ...` (re-run), followed by the validation CNAME block with non-empty `Name`/`Type`/`Value`. Re-run a second time to confirm idempotency — it must not create a duplicate cert.

- [ ] **Step 4: Commit**

```bash
git add scripts/setup-payment-tls.sh
git commit -m "feat(infra): request-cert subcommand"
```

---

## Task 6: `wait-cert` subcommand

**Files:**
- Modify: `scripts/setup-payment-tls.sh` — replace the `cmd_wait_cert` stub.

- [ ] **Step 1: Replace the stub**

Replace:

```bash
cmd_wait_cert()       { die "not implemented yet (Task 6)"; }
```

With:

```bash
cmd_wait_cert() {
  local arn
  arn=$(cert_arn_for_domain "$DOMAIN" "$REGION")
  [[ -n "$arn" ]] || die "no cert for $DOMAIN — run request-cert first"

  local deadline=$(( $(date +%s) + WAIT_CERT_TIMEOUT_SECONDS ))
  log "Waiting for cert $arn to be ISSUED (timeout ${WAIT_CERT_TIMEOUT_SECONDS}s)..."
  while :; do
    local status
    status=$(aws acm describe-certificate \
      --region "$REGION" \
      --certificate-arn "$arn" \
      --query 'Certificate.Status' --output text)
    log "  status=$status"
    case "$status" in
      ISSUED)  log "Cert issued."; return 0 ;;
      FAILED|VALIDATION_TIMED_OUT|REVOKED) die "cert ended in terminal status: $status" ;;
    esac
    if (( $(date +%s) >= deadline )); then
      die "timed out waiting for cert $arn"
    fi
    sleep 15
  done
}
```

- [ ] **Step 2: Verify syntax**

Run: `bash -n scripts/setup-payment-tls.sh`
Expected: exit 0.

- [ ] **Step 3: Live run (only after the validation CNAME is in DNS)**

Run: `./scripts/setup-payment-tls.sh wait-cert`
Expected: prints `status=PENDING_VALIDATION` a few times, then `status=ISSUED` and `Cert issued.`, exits 0. If the CNAME is not yet in place, the loop will keep printing PENDING_VALIDATION until timeout — that's fine. Ctrl-C is safe.

- [ ] **Step 4: Commit**

```bash
git add scripts/setup-payment-tls.sh
git commit -m "feat(infra): wait-cert subcommand"
```

---

## Task 7: `security-groups` subcommand

**Files:**
- Modify: `scripts/setup-payment-tls.sh` — replace the `cmd_security_groups` stub.

- [ ] **Step 1: Replace the stub**

Replace:

```bash
cmd_security_groups() { die "not implemented yet (Task 7)"; }
```

With:

```bash
cmd_security_groups() {
  local vpc_id
  vpc_id=$(vpc_of_instance "$INSTANCE_ID" "$REGION")
  [[ -n "$vpc_id" && "$vpc_id" != "None" ]] || die "could not resolve VPC for $INSTANCE_ID"
  log "VPC: $vpc_id"

  # ALB SG --------------------------------------------------------------------
  local alb_sg
  alb_sg=$(sg_id_by_name "$ALB_SG_NAME" "$vpc_id" "$REGION")
  if [[ -z "$alb_sg" ]]; then
    log "Creating SG $ALB_SG_NAME..."
    alb_sg=$(aws ec2 create-security-group \
      --region "$REGION" \
      --group-name "$ALB_SG_NAME" \
      --description "Ingress for ${NAME_PREFIX} ALB (443/80 from internet)" \
      --vpc-id "$vpc_id" \
      --query GroupId --output text)
    log "Created SG: $alb_sg"
  else
    log "SG $ALB_SG_NAME already exists: $alb_sg"
  fi

  # Public ingress rules on ALB SG. authorize-security-group-ingress is not
  # idempotent, so we swallow the duplicate error.
  for port in 443 80; do
    aws ec2 authorize-security-group-ingress \
      --region "$REGION" \
      --group-id "$alb_sg" \
      --ip-permissions "IpProtocol=tcp,FromPort=$port,ToPort=$port,IpRanges=[{CidrIp=0.0.0.0/0,Description=public-${port}}]" \
      2>&1 | grep -v 'InvalidPermission.Duplicate' || true
  done
  log "ALB SG ingress 443/80 ensured"

  # EC2 SG: allow TARGET_PORT from ALB SG -------------------------------------
  local ec2_sgs
  ec2_sgs=$(aws ec2 describe-instances \
    --region "$REGION" \
    --instance-ids "$INSTANCE_ID" \
    --query 'Reservations[0].Instances[0].SecurityGroups[].GroupId' \
    --output text)
  [[ -n "$ec2_sgs" ]] || die "EC2 $INSTANCE_ID has no security groups (?)"

  # Use the first SG on the instance as the one we modify.
  local ec2_sg
  ec2_sg=$(awk '{print $1}' <<<"$ec2_sgs")
  log "EC2 SG to modify: $ec2_sg"

  aws ec2 authorize-security-group-ingress \
    --region "$REGION" \
    --group-id "$ec2_sg" \
    --ip-permissions "IpProtocol=tcp,FromPort=${TARGET_PORT},ToPort=${TARGET_PORT},UserIdGroupPairs=[{GroupId=${alb_sg},Description=from-${NAME_PREFIX}-alb}]" \
    2>&1 | grep -v 'InvalidPermission.Duplicate' || true
  log "EC2 SG ingress :${TARGET_PORT} from ${alb_sg} ensured"

  # Warn (do not auto-revoke) if 0.0.0.0/0 is open on TARGET_PORT for that SG.
  if aws ec2 describe-security-groups \
       --region "$REGION" --group-ids "$ec2_sg" \
       --query "SecurityGroups[0].IpPermissions[?FromPort==\`${TARGET_PORT}\`].IpRanges[?CidrIp=='0.0.0.0/0'] | []" \
       --output text | grep -q .; then
    log "WARN: EC2 SG $ec2_sg still allows ${TARGET_PORT} from 0.0.0.0/0. Revoke manually after verifying the ALB works."
  fi

  echo "ALB_SG=$alb_sg"
  echo "EC2_SG=$ec2_sg"
}
```

- [ ] **Step 2: Verify syntax**

Run: `bash -n scripts/setup-payment-tls.sh`
Expected: exit 0.

- [ ] **Step 3: Live run**

Run: `./scripts/setup-payment-tls.sh security-groups`
Expected output (first run): logs `Creating SG payment-alb-sg...`, `ALB SG ingress 443/80 ensured`, `EC2 SG ingress :8080 from sg-... ensured`, prints `ALB_SG=sg-...` and `EC2_SG=sg-...`. Re-run: logs `SG payment-alb-sg already exists: sg-...`, no errors, same `ALB_SG`/`EC2_SG` output.

- [ ] **Step 4: Verify in AWS Console**

Open the EC2 console → Security Groups → `payment-alb-sg`. Confirm inbound rules: 443 from 0.0.0.0/0 and 80 from 0.0.0.0/0. Open the EC2 instance's existing SG → confirm an inbound rule `TCP 8080` sourced from `payment-alb-sg`.

- [ ] **Step 5: Commit**

```bash
git add scripts/setup-payment-tls.sh
git commit -m "feat(infra): security-groups subcommand"
```

---

## Task 8: `target-group` subcommand

**Files:**
- Modify: `scripts/setup-payment-tls.sh` — replace the `cmd_target_group` stub.

- [ ] **Step 1: Replace the stub**

Replace:

```bash
cmd_target_group()    { die "not implemented yet (Task 8)"; }
```

With:

```bash
cmd_target_group() {
  local vpc_id
  vpc_id=$(vpc_of_instance "$INSTANCE_ID" "$REGION")
  [[ -n "$vpc_id" && "$vpc_id" != "None" ]] || die "could not resolve VPC for $INSTANCE_ID"

  local tg_arn
  tg_arn=$(tg_arn_by_name "$TG_NAME" "$REGION")
  if [[ -z "$tg_arn" ]]; then
    log "Creating target group $TG_NAME..."
    tg_arn=$(aws elbv2 create-target-group \
      --region "$REGION" \
      --name "$TG_NAME" \
      --protocol HTTP --port "$TARGET_PORT" \
      --vpc-id "$vpc_id" \
      --target-type instance \
      --health-check-protocol HTTP \
      --health-check-path "$HEALTH_PATH" \
      --health-check-port traffic-port \
      --health-check-interval-seconds 30 \
      --health-check-timeout-seconds 5 \
      --healthy-threshold-count 2 \
      --unhealthy-threshold-count 2 \
      --matcher HttpCode=200 \
      --query 'TargetGroups[0].TargetGroupArn' --output text)
    log "Created TG: $tg_arn"
  else
    log "Target group $TG_NAME already exists: $tg_arn"
  fi

  log "Registering target ${INSTANCE_ID}:${TARGET_PORT}..."
  aws elbv2 register-targets \
    --region "$REGION" \
    --target-group-arn "$tg_arn" \
    --targets "Id=${INSTANCE_ID},Port=${TARGET_PORT}" >/dev/null
  log "Target registered."

  echo "TG_ARN=$tg_arn"
}
```

- [ ] **Step 2: Verify syntax**

Run: `bash -n scripts/setup-payment-tls.sh`
Expected: exit 0.

- [ ] **Step 3: Live run**

Run: `./scripts/setup-payment-tls.sh target-group`
Expected: first run logs `Creating target group payment-tg...` and `Target registered.`, prints `TG_ARN=arn:aws:elasticloadbalancing:...:targetgroup/payment-tg/...`. Re-run: `Target group payment-tg already exists: arn:...`, `Target registered.` (re-registration is a no-op when the target is already in the group).

- [ ] **Step 4: Commit**

```bash
git add scripts/setup-payment-tls.sh
git commit -m "feat(infra): target-group subcommand"
```

---

## Task 9: `alb` subcommand

**Files:**
- Modify: `scripts/setup-payment-tls.sh` — replace the `cmd_alb` stub.

- [ ] **Step 1: Replace the stub**

Replace:

```bash
cmd_alb()             { die "not implemented yet (Task 9)"; }
```

With:

```bash
cmd_alb() {
  local vpc_id
  vpc_id=$(vpc_of_instance "$INSTANCE_ID" "$REGION")
  [[ -n "$vpc_id" && "$vpc_id" != "None" ]] || die "could not resolve VPC for $INSTANCE_ID"

  local alb_sg
  alb_sg=$(sg_id_by_name "$ALB_SG_NAME" "$vpc_id" "$REGION")
  [[ -n "$alb_sg" ]] || die "$ALB_SG_NAME not found — run security-groups first"

  local subnets
  mapfile -t subnets < <(public_subnets_in_vpc "$vpc_id" "$REGION" 2)
  (( ${#subnets[@]} >= 2 )) || die "need 2 public subnets in different AZs; found ${#subnets[@]} in $vpc_id"
  log "Using subnets: ${subnets[*]}"

  local alb_arn
  alb_arn=$(alb_arn_by_name "$ALB_NAME" "$REGION")
  if [[ -z "$alb_arn" ]]; then
    log "Creating ALB $ALB_NAME..."
    alb_arn=$(aws elbv2 create-load-balancer \
      --region "$REGION" \
      --name "$ALB_NAME" \
      --type application --scheme internet-facing --ip-address-type ipv4 \
      --subnets "${subnets[@]}" \
      --security-groups "$alb_sg" \
      --query 'LoadBalancers[0].LoadBalancerArn' --output text)
    log "Created ALB: $alb_arn"
    log "Waiting for ALB to be active..."
    aws elbv2 wait load-balancer-available --region "$REGION" --load-balancer-arns "$alb_arn"
  else
    log "ALB $ALB_NAME already exists: $alb_arn"
  fi

  local dns
  dns=$(alb_dns_by_arn "$alb_arn" "$REGION")
  echo "ALB_ARN=$alb_arn"
  echo "ALB_DNS=$dns"
}
```

- [ ] **Step 2: Verify syntax**

Run: `bash -n scripts/setup-payment-tls.sh`
Expected: exit 0.

- [ ] **Step 3: Live run**

Run: `./scripts/setup-payment-tls.sh alb`
Expected: logs `Using subnets: subnet-... subnet-...`, then either `Creating ALB payment-alb...` followed by `Waiting for ALB to be active...` (takes 2-3 min on first run), or `ALB payment-alb already exists: ...`. Prints `ALB_ARN=...` and `ALB_DNS=payment-alb-xxxxx.ap-southeast-2.elb.amazonaws.com`.

- [ ] **Step 4: Commit**

```bash
git add scripts/setup-payment-tls.sh
git commit -m "feat(infra): alb subcommand"
```

---

## Task 10: `listeners` subcommand

**Files:**
- Modify: `scripts/setup-payment-tls.sh` — replace the `cmd_listeners` stub.

- [ ] **Step 1: Replace the stub**

Replace:

```bash
cmd_listeners()       { die "not implemented yet (Task 10)"; }
```

With:

```bash
cmd_listeners() {
  local alb_arn tg_arn cert_arn
  alb_arn=$(alb_arn_by_name "$ALB_NAME" "$REGION")
  tg_arn=$(tg_arn_by_name "$TG_NAME" "$REGION")
  cert_arn=$(cert_arn_for_domain "$DOMAIN" "$REGION")
  [[ -n "$alb_arn"  ]] || die "ALB $ALB_NAME not found — run alb first"
  [[ -n "$tg_arn"   ]] || die "TG $TG_NAME not found — run target-group first"
  [[ -n "$cert_arn" ]] || die "cert for $DOMAIN not found — run request-cert first"

  # Print existing listener ports so we can skip create when present.
  local existing
  existing=$(aws elbv2 describe-listeners \
    --region "$REGION" --load-balancer-arn "$alb_arn" \
    --query 'Listeners[].Port' --output text)
  log "Existing listener ports: ${existing:-<none>}"

  if ! grep -qw 443 <<<"$existing"; then
    log "Creating HTTPS:443 listener..."
    aws elbv2 create-listener \
      --region "$REGION" \
      --load-balancer-arn "$alb_arn" \
      --protocol HTTPS --port 443 \
      --ssl-policy "$SSL_POLICY" \
      --certificates "CertificateArn=$cert_arn" \
      --default-actions "Type=forward,TargetGroupArn=$tg_arn" >/dev/null
    log "HTTPS:443 listener created."
  else
    log "HTTPS:443 listener already exists; skipping."
  fi

  if ! grep -qw 80 <<<"$existing"; then
    log "Creating HTTP:80 redirect listener..."
    aws elbv2 create-listener \
      --region "$REGION" \
      --load-balancer-arn "$alb_arn" \
      --protocol HTTP --port 80 \
      --default-actions \
        'Type=redirect,RedirectConfig={Protocol=HTTPS,Port=443,Host=#{host},Path=/#{path},Query=#{query},StatusCode=HTTP_301}' >/dev/null
    log "HTTP:80 redirect listener created."
  else
    log "HTTP:80 listener already exists; skipping."
  fi
}
```

- [ ] **Step 2: Verify syntax**

Run: `bash -n scripts/setup-payment-tls.sh`
Expected: exit 0.

- [ ] **Step 3: Live run**

Run: `./scripts/setup-payment-tls.sh listeners`
Expected (first run): logs `Existing listener ports: `, then `HTTPS:443 listener created.` and `HTTP:80 redirect listener created.`. Re-run: `Existing listener ports: 443 80`, then both `already exists; skipping.` lines.

- [ ] **Step 4: Commit**

```bash
git add scripts/setup-payment-tls.sh
git commit -m "feat(infra): listeners subcommand"
```

---

## Task 11: `status` subcommand

**Files:**
- Modify: `scripts/setup-payment-tls.sh` — replace the `cmd_status` stub.

- [ ] **Step 1: Replace the stub**

Replace:

```bash
cmd_status()          { die "not implemented yet (Task 11)"; }
```

With:

```bash
cmd_status() {
  local cert_arn alb_arn tg_arn
  cert_arn=$(cert_arn_for_domain "$DOMAIN" "$REGION")
  alb_arn=$(alb_arn_by_name "$ALB_NAME" "$REGION")
  tg_arn=$(tg_arn_by_name "$TG_NAME" "$REGION")

  echo "=== payment TLS status ($REGION) ==="

  if [[ -n "$cert_arn" ]]; then
    local cert_status
    cert_status=$(aws acm describe-certificate \
      --region "$REGION" --certificate-arn "$cert_arn" \
      --query 'Certificate.Status' --output text)
    echo "Cert:    $cert_arn  ($cert_status)"
  else
    echo "Cert:    <not created>"
  fi

  if [[ -n "$alb_arn" ]]; then
    local dns
    dns=$(alb_dns_by_arn "$alb_arn" "$REGION")
    echo "ALB DNS: $dns"
    echo "         (add CNAME: $DOMAIN -> $dns)"
    aws elbv2 describe-listeners \
      --region "$REGION" --load-balancer-arn "$alb_arn" \
      --query 'Listeners[].{Port:Port,Protocol:Protocol}' --output table
  else
    echo "ALB:     <not created>"
  fi

  if [[ -n "$tg_arn" ]]; then
    echo "Target health:"
    aws elbv2 describe-target-health \
      --region "$REGION" --target-group-arn "$tg_arn" \
      --query 'TargetHealthDescriptions[].{Id:Target.Id,Port:Target.Port,State:TargetHealth.State,Reason:TargetHealth.Reason}' \
      --output table
  else
    echo "TG:      <not created>"
  fi
}
```

- [ ] **Step 2: Verify syntax**

Run: `bash -n scripts/setup-payment-tls.sh`
Expected: exit 0.

- [ ] **Step 3: Live run**

Run: `./scripts/setup-payment-tls.sh status`
Expected: prints cert ARN and `ISSUED` status, ALB DNS, the CNAME instruction, a listeners table (`Port 443 HTTPS`, `Port 80 HTTP`), and a target health table for `i-00dc197caba8ab3eb:8080`. The target may show `initial` for the first ~30s after registration, then `healthy`. If it stays `unhealthy`, the `Reason` column tells you why (e.g., `Target.FailedHealthChecks`).

- [ ] **Step 4: Commit**

```bash
git add scripts/setup-payment-tls.sh
git commit -m "feat(infra): status subcommand"
```

---

## Task 12: `all` subcommand

**Files:**
- Modify: `scripts/setup-payment-tls.sh` — replace the `cmd_all` stub.

- [ ] **Step 1: Replace the stub**

Replace:

```bash
cmd_all()             { die "not implemented yet (Task 12)"; }
```

With:

```bash
cmd_all() {
  cmd_request_cert
  log "------------------------------------------------------------"
  log "If you have not added the DNS validation CNAME printed above,"
  log "add it now. wait-cert will block until ACM sees it."
  log "------------------------------------------------------------"
  cmd_wait_cert
  cmd_security_groups
  cmd_target_group
  cmd_alb
  cmd_listeners
  cmd_status
}
```

- [ ] **Step 2: Verify syntax**

Run: `bash -n scripts/setup-payment-tls.sh`
Expected: exit 0.

- [ ] **Step 3: Smoke test (optional)**

If steps 5-11 already ran successfully, `./scripts/setup-payment-tls.sh all` should be a fast no-op that ends in the same `status` output. Re-running this subcommand is the documented full-run-from-clean-account path.

- [ ] **Step 4: Commit**

```bash
git add scripts/setup-payment-tls.sh
git commit -m "feat(infra): all subcommand"
```

---

## Task 13: End-to-end verification

No file changes — verifies the deployed infrastructure works.

- [ ] **Step 1: Add the `payment` CNAME at the DNS provider**

Take the `ALB_DNS` value from `status` and add at the external DNS provider:

```
payment.bunzodelivery.com  CNAME  <ALB_DNS>
```

- [ ] **Step 2: Wait for DNS to propagate, then test HTTPS**

Run: `curl -sv https://payment.bunzodelivery.com/actuator/health`
Expected: `HTTP/2 200`, valid TLS chain (`* SSL certificate verify ok.`), body returned by product-service.

- [ ] **Step 3: Verify HTTP→HTTPS redirect**

Run: `curl -sv http://payment.bunzodelivery.com/actuator/health`
Expected: `HTTP/1.1 301 Moved Permanently` with `Location: https://payment.bunzodelivery.com/actuator/health`.

- [ ] **Step 4: Verify path/query pass-through**

Pick any known product-service path (replace `<path>` below). Run inside the VPC or via SSH to the instance:
```bash
curl -sv "http://localhost:${TARGET_PORT:-8080}/<path>?x=1"
```
Then run from your laptop:
```bash
curl -sv "https://payment.bunzodelivery.com/<path>?x=1"
```
Expected: same response body for both.

- [ ] **Step 5: Verify lockdown (only if EC2 has a public IP)**

Run: `curl -m 5 -sv http://<ec2-public-ip>:8080/actuator/health`
Expected: connection times out or is refused. If you get a 200, the EC2's existing SG still has a public rule on 8080 — remove it manually after confirming the ALB path works.

- [ ] **Step 6: Capture the result in the spec doc**

Append a short "Deployment" note to the bottom of the spec file with the ALB DNS, cert ARN, and the date you completed verification. Commit.

```bash
git add docs/superpowers/specs/2026-05-30-payment-tls-termination-design.md
git commit -m "docs: record payment TLS deployment outcome"
```

---

## Self-review notes

- Spec coverage: every section of the spec maps to a task — components (Tasks 5-10), VPC/subnet discovery (Task 2 + used in Tasks 7/9), idempotency (built into 5-10), error handling (cert timeout in 6, SG duplicate handling in 7, subnet check in 9), runbook (Task 12 wires the full path), testing (Task 13). DNS provider step lives at Task 13 step 1 as the manual hand-off.
- No placeholders, no "TBD".
- Resource names are consistent: `payment-cert`, `payment-alb-sg`, `payment-tg`, `payment-alb`, `payment-alb-xxxxx.ap-southeast-2.elb.amazonaws.com`.
- Function names are consistent across the library and orchestrator (`vpc_of_instance`, `public_subnets_in_vpc`, `cert_arn_for_domain`, `sg_id_by_name`, `tg_arn_by_name`, `alb_arn_by_name`, `alb_dns_by_arn`).
