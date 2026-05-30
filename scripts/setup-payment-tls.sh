#!/usr/bin/env bash
# Set up TLS termination at payment.banzodelivery.com in front of an EC2 host.
# See docs/superpowers/specs/2026-05-30-payment-tls-termination-design.md
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=payment-tls-lib.sh
source "${SCRIPT_DIR}/payment-tls-lib.sh"

# --- Config (override via env) -----------------------------------------------
REGION="${AWS_REGION:-ap-southeast-2}"
INSTANCE_ID="${INSTANCE_ID:-i-00dc197caba8ab3eb}"
DOMAIN="${DOMAIN:-payment.banzodelivery.com}"
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

# Subcommand stubs — filled in by later tasks.
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
cmd_security_groups() {
  local vpc_id
  vpc_id=$(vpc_of_instance "$INSTANCE_ID" "$REGION")
  [[ -n "$vpc_id" && "$vpc_id" != "None" ]] || die "could not resolve VPC for $INSTANCE_ID"
  log "VPC: $vpc_id"

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

  for port in 443 80; do
    aws ec2 authorize-security-group-ingress \
      --region "$REGION" \
      --group-id "$alb_sg" \
      --ip-permissions "IpProtocol=tcp,FromPort=$port,ToPort=$port,IpRanges=[{CidrIp=0.0.0.0/0,Description=public-${port}}]" \
      2>&1 | grep -v 'InvalidPermission.Duplicate' || true
  done
  log "ALB SG ingress 443/80 ensured"

  local ec2_sgs
  ec2_sgs=$(aws ec2 describe-instances \
    --region "$REGION" \
    --instance-ids "$INSTANCE_ID" \
    --query 'Reservations[0].Instances[0].SecurityGroups[].GroupId' \
    --output text)
  [[ -n "$ec2_sgs" ]] || die "EC2 $INSTANCE_ID has no security groups (?)"

  local ec2_sg
  ec2_sg=$(awk '{print $1}' <<<"$ec2_sgs")
  log "EC2 SG to modify: $ec2_sg"

  aws ec2 authorize-security-group-ingress \
    --region "$REGION" \
    --group-id "$ec2_sg" \
    --ip-permissions "IpProtocol=tcp,FromPort=${TARGET_PORT},ToPort=${TARGET_PORT},UserIdGroupPairs=[{GroupId=${alb_sg},Description=from-${NAME_PREFIX}-alb}]" \
    2>&1 | grep -v 'InvalidPermission.Duplicate' || true
  log "EC2 SG ingress :${TARGET_PORT} from ${alb_sg} ensured"

  if aws ec2 describe-security-groups \
       --region "$REGION" --group-ids "$ec2_sg" \
       --query "SecurityGroups[0].IpPermissions[?FromPort==\`${TARGET_PORT}\`].IpRanges[?CidrIp=='0.0.0.0/0'] | []" \
       --output text | grep -q .; then
    log "WARN: EC2 SG $ec2_sg still allows ${TARGET_PORT} from 0.0.0.0/0. Revoke manually after verifying the ALB works."
  fi

  echo "ALB_SG=$alb_sg"
  echo "EC2_SG=$ec2_sg"
}
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
cmd_alb() {
  local vpc_id
  vpc_id=$(vpc_of_instance "$INSTANCE_ID" "$REGION")
  [[ -n "$vpc_id" && "$vpc_id" != "None" ]] || die "could not resolve VPC for $INSTANCE_ID"

  local alb_sg
  alb_sg=$(sg_id_by_name "$ALB_SG_NAME" "$vpc_id" "$REGION")
  [[ -n "$alb_sg" ]] || die "$ALB_SG_NAME not found — run security-groups first"

  local subnets=()
  while IFS= read -r line; do
    [[ -n "$line" ]] && subnets+=("$line")
  done < <(public_subnets_in_vpc "$vpc_id" "$REGION" 2)
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
cmd_listeners() {
  local alb_arn tg_arn cert_arn
  alb_arn=$(alb_arn_by_name "$ALB_NAME" "$REGION")
  tg_arn=$(tg_arn_by_name "$TG_NAME" "$REGION")
  cert_arn=$(cert_arn_for_domain "$DOMAIN" "$REGION")
  [[ -n "$alb_arn"  ]] || die "ALB $ALB_NAME not found — run alb first"
  [[ -n "$tg_arn"   ]] || die "TG $TG_NAME not found — run target-group first"
  [[ -n "$cert_arn" ]] || die "cert for $DOMAIN not found — run request-cert first"

  local cert_status
  cert_status=$(aws acm describe-certificate \
    --region "$REGION" --certificate-arn "$cert_arn" \
    --query 'Certificate.Status' --output text)
  [[ "$cert_status" == "ISSUED" ]] || die "cert is $cert_status (need ISSUED) — run wait-cert first"

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

main "$@"
