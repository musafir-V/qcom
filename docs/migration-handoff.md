# AWS Account Migration — Session Handoff

**Date:** 2026-06-11  
**Goal:** Migrate Bunzo qcom stack from old AWS account → new admin account. Same resource names; cutover = DNS only.

---

## Accounts

| | Account ID | Credentials file |
|---|---|---|
| **Old** | `119312949433` | `.deploy.local.env` |
| **New** | `078455283887` | `.deploy.new.env` |

**Region:** `ap-southeast-2` (both)

---

## What's done

### Phase 1 — Foundation
- VPC `vpc-023f7f8f06f9368a8`, 3 subnets
- RDS subnet group `bunzo-db-subnet-group`
- S3 **skipped** (`printdrop-documents` not migrated — intentional)

### Phase 2 — Data
| Resource | Status |
|---|---|
| DynamoDB `QComTable` | 955 items migrated |
| SSM `/qcom/prod/*` | 16 params copied verbatim |
| RDS `quickcommerce` | Restored; DB `inventory`; endpoint `quickcommerce.c9se0yaqs4di.ap-southeast-2.rds.amazonaws.com` |

### Phase 3 — Compute (new account)

| Service | Instance | ALB DNS | Health |
|---|---|---|---|
| **qcom** | `i-0cfb8e08ac9f45413` | `qcom-alb-1260677250.ap-southeast-2.elb.amazonaws.com` | HTTP `/health` → `OK` |
| **product-service** | `i-0129ae085e7a35be2` | `payment-alb-1973051114.ap-southeast-2.elb.amazonaws.com` | HTTP `/actuator/health` → `UP` |

**qcom stack:** `qcom-asg`, `qcom-alb`, `qcom-tg`, `qcom-lt`, `qcom-ec2-role`, `qcom-ec2-profile`  
**payment stack:** `payment-alb`, `payment-tg`, `payment-alb-sg`, `product-ec2-sg`

**product-service migration method:** AMI snapshot from old `i-00dc197caba8ab3eb` → copied to new account → launched with user-data swapping RDS hostname. Java/Docker app preserved.

**Old account still live** — DNS still points to old ALBs. `payment.bunzodelivery.com` still hits old account until cutover.

---

## What's NOT done

### Phase 5 — DNS cutover (user action in Squarespace)

**ACM validation CNAMEs** (add first — certs are `PENDING_VALIDATION`):

| Name | Value |
|---|---|
| `_216ed7ae37fa518ca1f437839329dfb4.api` | `_104e00023dfbc7853cff0d81e4a87f25.jkddzztszm.acm-validations.aws` |
| `_bd1d0f1d733d1145aa6988903c83ddd1.payment` | `_497494834f3ae9eb36af5f106cffff6b.jkddzztszm.acm-validations.aws` |

**Cutover CNAMEs** (after ACM `ISSUED` + HTTPS listeners):

| Record | Point to |
|---|---|
| `api` | `qcom-alb-1260677250.ap-southeast-2.elb.amazonaws.com` |
| `payment` | `payment-alb-1973051114.ap-southeast-2.elb.amazonaws.com` |

Then run:
```bash
source .deploy.new.env
bash scripts/migration/06-qcom-https.sh
INSTANCE_ID=i-0129ae085e7a35be2 bash scripts/setup-payment-tls.sh listeners
cp .deploy.new.env .deploy.local.env   # point make deploy at new account
```

### Phase 6 — Decommission old account (after 48h soak)

---

## Smoke tests (against new ALB)

```bash
SMOKE_BASE_URL=http://qcom-alb-1260677250.ap-southeast-2.elb.amazonaws.com make test-smoke
```

**Last run: 4 pass, 3 fail**

| Test | Result | Root cause |
|---|---|---|
| Health, StoreQR, CustomerAuthFlow, AuthErrors | Pass | — |
| AddressLifecycle | Fail | **Missing DynamoDB GSIs** on new `QComTable` — `UserIdIndex` was null; `create-table.sh` re-run started GSI creation |
| Serviceability | Fail | Same GSI issue (`GetMyAddresses` uses `UserIdIndex`); also **0 darkstore records** in migrated DDB |
| DEFlow | Fail | **Smoke test outdated** — API requires `driver_license_url`; test only sends `profile_url` + `nrc_url` |

### Fix smoke failures (next session)

1. **Wait for GSIs ACTIVE**, then add missing indexes if needed:
   ```bash
   source .deploy.new.env
   DYNAMODB_REGION=ap-southeast-2 ./scripts/create-table.sh
   aws dynamodb describe-table --table-name QComTable --region ap-southeast-2 \
     --query 'Table.GlobalSecondaryIndexes[*].{Name:IndexName,Status:IndexStatus}'
   ```
   Expected: `UserIdIndex`, `DEDutyIndex`, `ReferralCodeIndex` all `ACTIVE`.

2. **Seed darkstores** (serviceability needs them even with `IS_TEST=true` if `ListActive` returns empty — verify):
   ```bash
   ./scripts/seed-darkstores.sh   # against new account creds
   ```

3. **Update smoke test** `registerDE()` to include `driver_license_url` (or use dummy URL since S3 dropped).

---

## Code changes made this session (uncommitted)

| File | Change |
|---|---|
| `scripts/fetch-env.sh` | SSM pagination fix (was only loading 10/16 params) |
| `scripts/ec2-bootstrap.sh` | Paginated SSM inline; gzip/nohup-friendly; CloudWatch skipped; console tee |
| `scripts/migration/*` | Migration automation scripts |
| `.deploy.new.env` | New account config + naming contract |
| `.gitignore` | Ignore `.deploy.*.env`, migration data exports |
| `docs/superpowers/plans/2026-06-11-aws-account-migration.md` | Full plan updated |
| `data/migration-status.json` | Machine-readable status |

---

## Bootstrap issues encountered & fixes

| Issue | Fix |
|---|---|
| SSM only 10 params loaded | Pagination in `fetch-env.sh` + bootstrap |
| IAM `ssm:GetParametersByPath` denied on `/qcom/prod` | Policy must include both `/qcom/prod` and `/qcom/prod/*` |
| Cloud-init 120s timeout | `scripts/migration/make-userdata.sh` — gzip + nohup stub |
| User-data >16KB | Gzip compress bootstrap |
| RDS cross-account encrypted snapshot | Re-encrypt with customer CMK in old account |
| product-service AMI copy denied | Share EBS snapshot with new account ID |
| No SSH key for product-service | AMI migration instead |

---

## Key files for next agent

| File | Purpose |
|---|---|
| `.deploy.new.env` | New account creds + resource names (gitignored) |
| `data/migration-status.json` | Current resource IDs/DNS |
| `data/migration-foundation.json` | VPC/subnet IDs |
| `data/migration-manifest.json` | Old account inventory |
| `docs/superpowers/plans/2026-06-11-aws-account-migration.md` | Full migration plan |
| `scripts/migration/06-qcom-https.sh` | Attach HTTPS after ACM issued |
| `scripts/setup-payment-tls.sh` | payment-alb listeners |

---

## Backward compatibility rules (do not break)

- Same names: `QComTable`, `quickcommerce`, `qcom-asg`, `qcom-alb`, `payment-alb`, etc.
- SSM values unchanged (especially `JWT_SECRET_KEY`, `JAVA_ORDER_SERVICE_URL` domain URLs)
- Cutover = DNS only; apps keep using `api.bunzodelivery.com` / `payment.bunzodelivery.com`
- Server-side only change: product-service JDBC host (done via AMI user-data)

---

## Suggested next steps (in order)

1. Confirm `UserIdIndex` (+ other GSIs) are `ACTIVE` on new `QComTable`
2. Run `seed-darkstores.sh` on new account if serviceability still fails
3. Re-run smoke tests against new ALB
4. User adds 4 Squarespace CNAMEs (2 ACM validation + 2 cutover)
5. Run `06-qcom-https.sh` + `setup-payment-tls.sh listeners`
6. DNS cutover for `api` and `payment`
7. Verify production: `curl https://api.bunzodelivery.com/health`
8. Decommission old account after soak

---

## Quick health checks

```bash
# New account (HTTP, pre-DNS)
curl http://qcom-alb-1260677250.ap-southeast-2.elb.amazonaws.com/health
curl http://payment-alb-1973051114.ap-southeast-2.elb.amazonaws.com/actuator/health

# Old account (still production via DNS)
curl https://api.bunzodelivery.com/health
curl https://payment.bunzodelivery.com/actuator/health
```
