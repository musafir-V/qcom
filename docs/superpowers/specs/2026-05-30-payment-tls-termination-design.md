# Payment TLS termination — design

**Date:** 2026-05-30
**Author:** musafir-V (via Claude Code brainstorming)
**Status:** Draft, awaiting user review

## Goal

Expose the `product-service` running on EC2 instance `i-00dc197caba8ab3eb:8080` (region `ap-southeast-2`) to the public internet at `https://payment.bunzodelivery.com`, with TLS terminated at the edge so the EC2 host itself continues to speak plain HTTP. The setup must:

- Forward path, query string, and method to the backend unchanged.
- Use an AWS-managed certificate (auto-renewing).
- Lock backend port 8080 down so it is reachable only from the load balancer, not from the public internet.
- Be reproducible from shell scripts that match the existing pattern in `scripts/`.

Out of scope: WAF, access logging, sticky sessions, multi-region failover, blue/green, autoscaling the backend.

## Approach

**Application Load Balancer + ACM (selected).** AWS-managed ALB in `ap-southeast-2`, HTTPS listener with an ACM certificate, forwarding plain HTTP to the EC2 instance on port 8080. DNS is managed at an external provider, so the user adds two CNAMEs by hand: one for ACM DNS validation, one for `payment` → ALB DNS name.

Alternatives considered and rejected:

- **CloudFront + ACM** — adds an edge layer the workload doesn't need; cert must live in `us-east-1`; payment traffic should not be cached.
- **nginx on the EC2 with Let's Encrypt** — cheapest, but TLS would live on the EC2 (the user explicitly wants TLS off the host) and certificate renewal becomes our responsibility.

## Architecture

```
Client
   │  HTTPS payment.bunzodelivery.com
   ▼
External DNS provider
   │  CNAME → <alb-dns-name>.elb.ap-southeast-2.amazonaws.com
   ▼
Application Load Balancer  (ap-southeast-2, internet-facing, 2 AZs)
   ├─ Listener :443 HTTPS — ACM cert for payment.bunzodelivery.com
   └─ Listener :80  HTTP  — redirect 301 to HTTPS
                                │
                                │ forward (plain HTTP, path/query unchanged)
                                ▼
                          Target Group  (type=instance, port 8080)
                          Health check: GET /actuator/health, matcher 200
                                │
                                ▼
                          EC2 i-00dc197caba8ab3eb (product-service:8080)
                          SG ingress 8080 only from payment-alb-sg
```

## Components

Listed in creation order. The orchestrator script (see Scripts) executes them in this sequence; each step is idempotent.

### 1. VPC and subnet discovery
Read the VPC of `i-00dc197caba8ab3eb`. Pick two **public** subnets in different AZs in that VPC. If only one public subnet exists, stop and surface the error — ALB requires at least two AZs.

### 2. ACM certificate (`payment-cert`)
- Region: `ap-southeast-2`.
- Domain: `payment.bunzodelivery.com`.
- Validation method: `DNS`.
- Script outputs the one validation CNAME (name + value) for the user to add at the DNS provider, then polls `aws acm describe-certificate` until `Status=ISSUED`.

### 3. Security groups
- **Create `payment-alb-sg`** in the same VPC. Ingress: `tcp/443` from `0.0.0.0/0`, `tcp/80` from `0.0.0.0/0`. Egress: all.
- **Modify the EC2's existing security group**: add ingress rule `tcp/8080` sourced from `payment-alb-sg`. Wrapped to ignore `InvalidPermission.Duplicate` so re-runs are safe.

This is the lockdown step: with the new rule in place, the EC2's port 8080 is reachable only from the ALB. The existing SG should not have a public `0.0.0.0/0` rule on 8080 after this; the script will warn if one is found.

### 4. Target group (`payment-tg`)
- Type: `instance`, protocol `HTTP`, port `8080`, VPC = the EC2's VPC.
- Health check: protocol `HTTP`, path `/actuator/health`, port `traffic-port`, matcher `200`, healthy threshold `2`, unhealthy threshold `2`, interval `30s`, timeout `5s`.
- Register target `i-00dc197caba8ab3eb` on port `8080`.

### 5. Application Load Balancer (`payment-alb`)
- Scheme: `internet-facing`.
- IP address type: `ipv4`.
- Subnets: the two public subnets from step 1.
- Security group: `payment-alb-sg`.
- Output: `DNSName` and `CanonicalHostedZoneId`.

### 6. Listeners
- **HTTPS `:443`** — certificate = ACM ARN from step 2; SSL policy `ELBSecurityPolicy-TLS13-1-2-2021-06`; default action `forward → payment-tg`.
- **HTTP `:80`** — default action `redirect` to HTTPS, port `443`, status `HTTP_301`, host/path/query preserved.

### 7. DNS (manual at external provider)
Add CNAME `payment.bunzodelivery.com` → `<alb-dns-name>.elb.ap-southeast-2.amazonaws.com`.

## Data flow

A request to `https://payment.bunzodelivery.com/foo/bar?x=1`:

1. Client resolves the CNAME to the ALB DNS, then to one of the ALB's AZ IPs.
2. TLS handshake terminates at the ALB using the ACM cert.
3. ALB forwards over plain HTTP inside AWS to the target group.
4. Target group sends the request to `i-00dc197caba8ab3eb:8080` with the original method, path (`/foo/bar`), query (`x=1`), body, and headers, plus `X-Forwarded-For`, `X-Forwarded-Proto: https`, `X-Forwarded-Port: 443`, and `X-Forwarded-Host: payment.bunzodelivery.com`.
5. product-service responds; ALB writes the response back over the TLS connection.

The ALB does no path rewriting and no header stripping beyond the standard ELB-managed headers.

## Error handling

Concerns are at the script level, not the runtime path (the runtime path is just ALB → EC2).

- **Cert never validates.** `wait-cert` polls with a timeout (default 30 min); on timeout the script exits non-zero with the CNAME the user still needs to add.
- **Only one public subnet.** Script exits non-zero before creating anything; instructions tell the user to create a second public subnet in another AZ.
- **EC2 not in target group's VPC.** Cannot happen because target group's VPC is derived from the instance.
- **Backend unhealthy.** Target group reports `unhealthy`; `status` subcommand surfaces the reason (`Target.FailedHealthChecks`, `Target.Timeout`, etc.) so the user knows whether product-service is up or the SG rule is missing.
- **Re-runs.** Every create step checks for existing resources by name (and by domain for the cert) and skips if found. SG rule creation tolerates `InvalidPermission.Duplicate`.

## Testing

Manual verification after setup, since this is one-off infrastructure:

1. `./scripts/setup-payment-tls.sh status` — prints cert ARN, ALB DNS, target health. Target must be `healthy`.
2. `curl -v https://payment.bunzodelivery.com/actuator/health` — expect `HTTP/2 200` and valid TLS chain.
3. `curl -v http://payment.bunzodelivery.com/actuator/health` — expect `301` to the HTTPS URL.
4. `curl -v https://payment.bunzodelivery.com/some/known/product-service/path` — expect the same response product-service returns directly on `:8080` from inside the VPC.
5. If the EC2 has a public IP, from outside AWS attempt `curl -v http://<ec2-public-ip>:8080/` — expect timeout / refused, confirming the lockdown. (Skip if the EC2 is private-only — the lockdown is already implied.)

## Scripts

Two new files under `scripts/`. No existing files change.

### `scripts/setup-payment-tls.sh`

Single orchestrator with subcommands. Config at the top, overridable by env var:

```bash
REGION="${AWS_REGION:-ap-southeast-2}"
INSTANCE_ID="${INSTANCE_ID:-i-00dc197caba8ab3eb}"
DOMAIN="${DOMAIN:-payment.bunzodelivery.com}"
TARGET_PORT="${TARGET_PORT:-8080}"
HEALTH_PATH="${HEALTH_PATH:-/actuator/health}"
NAME_PREFIX="${NAME_PREFIX:-payment}"
```

Subcommands:

| Subcommand        | Action                                                                     |
| ----------------- | -------------------------------------------------------------------------- |
| `request-cert`    | Step 2 — create cert if absent, print validation CNAME, exit.              |
| `wait-cert`       | Poll ACM until `Status=ISSUED` (default 30 min timeout).                   |
| `security-groups` | Step 3 — create `payment-alb-sg`, add `:8080` ingress from it to EC2 SG.   |
| `target-group`    | Step 4 — create TG, register target.                                       |
| `alb`             | Step 5 — create ALB, print DNS name.                                       |
| `listeners`       | Step 6 — create HTTPS and HTTP listeners.                                  |
| `status`          | Print cert status, ALB DNS, target health, listener summary.               |
| `all`             | `request-cert → wait-cert → security-groups → target-group → alb → listeners`. After `request-cert` it prints the validation CNAME and blocks on `wait-cert`. |

Naming: every created resource is prefixed `payment-` (`payment-cert`, `payment-alb-sg`, `payment-tg`, `payment-alb`) so it is trivial to find or tear down later.

Idempotency:
- ACM cert — look up by domain via `aws acm list-certificates`.
- SG — `describe-security-groups` by name.
- SG rules — `authorize-security-group-ingress` wrapped to swallow `InvalidPermission.Duplicate`.
- Target group, ALB, listeners — `describe-*` by name/ARN.

### `scripts/payment-tls-lib.sh`

Sourced helpers (`log`, `require_cmd`, `vpc_of_instance`, `public_subnets_in_vpc`, `cert_arn_for_domain`, `sg_id_by_name`, `tg_arn_by_name`, `alb_arn_by_name`). Keeps the orchestrator readable.

## User runbook

1. Run `./scripts/setup-payment-tls.sh request-cert`. Copy the validation CNAME printed and add it at the DNS provider.
2. Run `./scripts/setup-payment-tls.sh wait-cert`. Wait until it reports `ISSUED`.
3. Run `./scripts/setup-payment-tls.sh all` (or the remaining subcommands one by one).
4. Add the CNAME `payment.bunzodelivery.com → <ALB DNS name>` at the DNS provider (the script prints the exact value).
5. Verify with `curl -v https://payment.bunzodelivery.com/actuator/health`.

## Open questions

None blocking. To revisit later: WAF in front of the ALB, access logging to S3, and whether `payment-alb` should be promoted to a shared multi-service ALB if more services move behind it.
