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
cmd_wait_cert()       { die "not implemented yet (Task 6)"; }
cmd_security_groups() { die "not implemented yet (Task 7)"; }
cmd_target_group()    { die "not implemented yet (Task 8)"; }
cmd_alb()             { die "not implemented yet (Task 9)"; }
cmd_listeners()       { die "not implemented yet (Task 10)"; }
cmd_status()          { die "not implemented yet (Task 11)"; }
cmd_all()             { die "not implemented yet (Task 12)"; }

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
