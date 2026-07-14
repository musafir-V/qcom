# qcom API monitoring (Grafana Cloud)

Per-endpoint **API latency, success rate, and error rate** for the qcom Go
service, using the RED method (Rate, Errors, Duration).

## How it works

```
qcom (:2112 /metrics, loopback only)  ->  Grafana Alloy (per host)  ->  remote_write  ->  Grafana Cloud (Prometheus + Grafana)
```

- qcom exposes a single Prometheus histogram, `http_request_duration_seconds`,
  labelled `method`, `route` (the gorilla/mux path **template**, e.g.
  `/api/v1/drivers/{phone}`), and `status` (numeric code). See
  `internal/metrics/metrics.go` and `internal/middleware/metrics_middleware.go`.
- The metrics endpoint is served on a **separate server bound to
  `127.0.0.1:2112`** (configurable via `METRICS_PORT`), so it is never reachable
  through the public ALB. `/health` is excluded from metrics to keep ALB
  health-check noise out of the dashboards.
- **Grafana Alloy** runs on each host, scrapes `127.0.0.1:2112`, tags samples
  with `job="qcom"`, `env` (staging/production), and `instance` (hostname), and
  remote-writes to Grafana Cloud.

## What's in this directory

| Path | Purpose |
|------|---------|
| `alloy/config.alloy` | Alloy scrape + remote-write config (reads secrets from env) |
| `alloy/install-alloy.sh` | Installs/configures Alloy on an Ubuntu host; pulls creds from SSM |
| `grafana/qcom-api-dashboard.json` | The dashboard (import into Grafana Cloud) |

---

## One-time setup (human-in-the-loop)

These steps require your Grafana Cloud account and AWS prod/staging access, so
they are **not** automated here.

### 1. Create the Grafana Cloud stack

1. Sign up / create a stack at <https://grafana.com/>.
2. In the stack, open **Connections → Add new connection → Prometheus / Hosted
   Prometheus** (or **Prometheus remote_write**) and note:
   - **Remote write endpoint URL** (e.g. `https://prometheus-prod-XX-...grafana.net/api/prom/push`)
   - **Username / instance ID** (a numeric ID)
   - a **Grafana Cloud API token** (Access Policy token with `metrics:write`)

### 2. Store the credentials in SSM (both AWS accounts)

Put these in SSM under the existing `/qcom/prod/*` prefix (the prefix is
literally `/qcom/prod/` even in the staging account). Do this in **both** the
staging account (`850141023618`) and the production account (`078455283887`):

```bash
aws ssm put-parameter --type SecureString --name /qcom/prod/GRAFANA_CLOUD_PROM_URL     --value 'https://prometheus-prod-XX-...grafana.net/api/prom/push'
aws ssm put-parameter --type SecureString --name /qcom/prod/GRAFANA_CLOUD_PROM_USER    --value '1234567'
aws ssm put-parameter --type SecureString --name /qcom/prod/GRAFANA_CLOUD_PROM_API_KEY --value 'glc_xxx...'
```

> The EC2 instance role must allow `ssm:GetParameter` (+ KMS decrypt) for these
> parameters — the same role qcom already uses for `/qcom/prod/*`.

### 3. Deploy the instrumented qcom build

The metrics code ships with the normal qcom deploy — no separate step.

- **Staging** (single box, in place):

  ```bash
  ssh -i ~/.ssh/qcom-staging-key.pem ubuntu@13.55.140.91
  sudo -u qcom git -C /app/qcom fetch origin main
  sudo -u qcom git -C /app/qcom reset --hard origin/main
  sudo -u qcom env HOME=/app PATH=/usr/local/go/bin:$PATH make -C /app/qcom build
  sudo /app/qcom/scripts/fetch-env.sh
  sudo systemctl restart qcom
  # verify metrics are being produced locally (should list http_request_duration_seconds):
  curl -s 127.0.0.1:2112/metrics | grep http_request_duration_seconds | head
  ```

- **Production** (ASG rolling replace):

  ```bash
  cd qcom && make deploy
  ```

### 4. Install Alloy on the hosts

- **Staging** (run once on the box):

  ```bash
  cd /app/qcom/monitoring/alloy
  sudo QCOM_ENV=staging ./install-alloy.sh
  systemctl status alloy
  ```

- **Production** (ASG): add the install to the instance bootstrap so every new
  ASG instance self-installs Alloy. In the user-data / bootstrap script that
  already builds qcom from source, append (after the qcom checkout exists):

  ```bash
  QCOM_ENV=production /app/qcom/monitoring/alloy/install-alloy.sh
  ```

  Then trigger an ASG instance refresh (or the next `make deploy`) so instances
  pick it up. Existing instances can be bootstrapped by running the same command
  over SSH.

### 5. Import the dashboard

In Grafana Cloud: **Dashboards → New → Import → Upload JSON file**, choose
`grafana/qcom-api-dashboard.json`, and select your Prometheus data source when
prompted. Use the **Environment / Instance / Route** dropdowns at the top to
filter; the **Per-endpoint summary** table is the "each API" view.

---

## Verifying end-to-end

1. On a host: `curl -s 127.0.0.1:2112/metrics | grep http_request_duration_seconds`
   returns samples.
2. `journalctl -u alloy -f` shows successful remote_write (no 401/403).
3. In Grafana Cloud **Explore**, run
   `sum(rate(http_request_duration_seconds_count{job="qcom"}[5m]))` — you should
   see traffic within ~1–2 minutes.

## Configuration reference

| Env var (qcom) | Default | Meaning |
|----------------|---------|---------|
| `METRICS_PORT` | `2112` | Port for the loopback-only `/metrics` server |

| Env var (Alloy, via `/etc/default/alloy`) | Source | Meaning |
|-------------------------------------------|--------|---------|
| `QCOM_ENV` | install arg | `staging` or `production` (becomes the `env` label) |
| `GRAFANA_CLOUD_PROM_URL` | SSM | remote_write endpoint |
| `GRAFANA_CLOUD_PROM_USER` | SSM | remote_write username / instance ID |
| `GRAFANA_CLOUD_PROM_API_KEY` | SSM | remote_write API token |

## Notes & tradeoffs

- **Latency buckets** are capped at 2s (`0.01, 0.025, 0.05, 0.1, 0.15, 0.2, 0.3,
  0.5, 0.75, 1, 1.5, 2`). Requests slower than 2s land in the implicit `+Inf`
  bucket, so `histogram_quantile` reports "≥2s" rather than a precise value
  above 2s. Widen the buckets in `internal/metrics/metrics.go` if you need
  resolution beyond 2s.
- **Cardinality**: `route` uses the mux template and unmatched paths collapse to
  `route="unmatched"`, keeping active series bounded (~7–8k, within the Grafana
  Cloud free tier). If you approach the limit, collapse `status` to a status
  class label.
- **Alerting** is intentionally out of scope for this first pass — dashboards
  only. The same metric supports alert rules (high 5xx rate, high p95, target
  down) as a follow-up.
