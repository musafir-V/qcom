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

  local subnets_json
  subnets_json=$(aws ec2 describe-subnets \
    --region "$region" \
    --filters "Name=vpc-id,Values=$vpc_id" \
    --query 'Subnets[].{Id:SubnetId,Az:AvailabilityZone}' \
    --output json)

  local rts_json
  rts_json=$(aws ec2 describe-route-tables \
    --region "$region" \
    --filters "Name=vpc-id,Values=$vpc_id" \
    --output json)

  jq -r --argjson rts "$rts_json" --argjson n "$max" '
    . as $subnets
    | [ $subnets[] | . as $s | $s + {
        public: (
          ( [ $rts.RouteTables[]
              | select( (.Associations // [])
                        | any(.SubnetId == $s.Id) ) ]
            + [ $rts.RouteTables[]
                | select( (.Associations // [])
                          | any(.Main == true) ) ]
          )
          | .[0]
          | (.Routes // [])
          | any(.GatewayId? // "" | startswith("igw-"))
        )
      } ]
    | map(select(.public == true))
    | group_by(.Az) | map(.[0])
    | .[:$n]
    | .[].Id
  ' <<<"$subnets_json"
}

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
