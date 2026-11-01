# Observability Stack (SokaLens production)

Self-hosted metrics + logs + uptime for the production deployment. Everything
runs under the **`observability`** Compose profile and is reachable only on
`127.0.0.1`, so it is never exposed to the public internet. Access it from a
desktop over an SSH tunnel.

| Service | Image | Host port (127.0.0.1) | Purpose |
|---|---|---|---|
| Grafana | `grafana/grafana:11.6.0` | **3004** | Dashboards (Prometheus + Loki, provisioned) |
| Prometheus | `prom/prometheus:v3.7.0` | **9090** | Metrics store / scraper |
| node-exporter | `prom/node-exporter:v1.11.0` | **9100** | Host CPU/mem/disk/network metrics |
| cAdvisor | `ghcr.io/google/cadvisor:v0.60.5` | **8081** | Per-container metrics |
| Loki | `grafana/loki:3.6.0` | **3100** | Log store |
| Promtail | `grafana/promtail:3.6.0` | — (internal) | Ships Docker logs → Loki |
| Uptime Kuma | `louislam/uptime-kuma:1` | **3002** | External uptime monitors (pre-existing) |

## What is monitored

- **Host metrics (node-exporter → Prometheus):** CPU, memory, disk, network
  throughput, load average, and uptime of the VPS. Retention: 15 days.
- **Container metrics (cAdvisor → Prometheus):** per-container CPU, memory,
  network I/O, and filesystem I/O for every running container (`api`, `worker`,
  `web`, `admin`, `postgres`, `redis`, and the observability stack itself),
  identified by the `container_label_com_docker_compose_service` label.
- **Logs (Promtail → Loki):** Docker json-file logs from the `api`, `worker`,
  `web`, and `admin` containers, tailed via the Docker socket. Retention:
  31 days.
- **Not scraped:** the Go API's Prometheus endpoint
  (`/v1/admin/observability/metrics`) is admin-gated (admin session +
  `read_analytics` permission), so Prometheus does not scrape it — host and
  container metrics come from node-exporter and cAdvisor instead.

## How to see your services

### 1. Open an SSH tunnel

From a local terminal, forward the ports. `sokalens-vps` is a `Host` entry
in your own `~/.ssh/config` holding the address, port, user and key; none of
those belong in this repository.

```bash
ssh -L 3004:127.0.0.1:3004 \
  -L 9090:127.0.0.1:9090 \
  -L 3100:127.0.0.1:3100 \
  sokalens-vps
```

### 2. Open a dashboard

- Grafana: <http://localhost:3004> (admin credentials from `GF_SECURITY_ADMIN_USER`
  / `GF_SECURITY_ADMIN_PASSWORD` in `.env.prod`).
- Go to **Dashboards → SokaLens** (the provisioned folder) and pick:
  - **Node Exporter Full** — host CPU / memory / disk / network / load / uptime.
  - **cAdvisor** — per-container CPU, memory, network, and disk I/O.

### 3. See each service (api / worker / web / admin / postgres / redis)

Open the **cAdvisor** dashboard. Use the **Container** dropdown at the top to
filter by service — select `api`, `worker`, `web`, `admin`, `postgres`, or
`redis` (multi-select is enabled; "All" shows every container). The legend lists
each container by its Compose service name.

### 4. Check what Prometheus is scraping

- Prometheus UI: <http://localhost:9090> → **Status → Targets**. All of
  `prometheus`, `node-exporter`, and `cadvisor` should be **UP**.
- Or on the VPS: `curl -s http://127.0.0.1:9090/api/v1/targets | jq '.data.activeTargets[] | {labels, health}'`

### 5. Query logs in Loki

In Grafana open **Explore**, select the **Loki** datasource, and run a query like:

```logql
{container="api"}
```

Replace `api` with `worker`, `web`, or `admin`. Add a filter, e.g.
`{container="api"} |= "error"`, and set the time range to the top-right of
Explore.

## Deploy

From `~/sokalens` on the VPS (observability is a separate profile — it does not
recreate the core/search/frontend stack):

```bash
docker compose --env-file .env.prod -f infra/docker/docker-compose.yml \
  --profile observability up -d

docker compose --env-file .env.prod -f infra/docker/docker-compose.yml \
  --profile observability ps
```

After changing dashboard files or the Grafana datasource config, recreate only
Grafana:

```bash
docker compose --env-file .env.prod -f infra/docker/docker-compose.yml \
  --profile observability up -d --force-recreate grafana
```

Do **not** use `--profile all` — that would recreate the running web/admin/api
services.

## Credentials

- **Grafana admin user/password** come from `GF_SECURITY_ADMIN_USER` and
  `GF_SECURITY_ADMIN_PASSWORD` in `.env.prod` (the Compose service maps them to
  Grafana's `GF_SECURITY_*` variables). No password is committed. On first
  login Grafana may ask you to change it depending on the value set.
- **Loki** runs with `auth_enabled: false` — it is not exposed publicly, so
  auth is handled by the `127.0.0.1` bind + SSH tunnel.
- **Prometheus/Promtail** have no auth; same `127.0.0.1`-only model.

## Health checks

```bash
curl -s http://127.0.0.1:9090/-/healthy            # Prometheus -> "Prometheus Server is Healthy."
curl -s http://127.0.0.1:3100/ready                # Loki -> "ready"
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:3004/login   # Grafana -> 200
```

## Grafana provisioning

Provisioned read-only, applied on container start:

- **Datasources** (`grafana/provisioning/datasources/datasources.yml`):
  - **Prometheus** — `http://prometheus:9090` (default, UID `prometheus`)
  - **Loki** — `http://loki:3100` (UID `loki`)
- **Dashboards** (`grafana/provisioning/dashboards/`): the provider in
  `dashboards.yml` loads every `*.json` in the folder into the **SokaLens**
  dashboard folder. The Compose file mounts
  `../observability/grafana/provisioning` to `/etc/grafana/provisioning`, so
  dashboards appear automatically after Grafana is (re)created.

## Extending the log scope

Promtail keeps only `api|worker|web|admin` containers (see the
`relabel_configs` regex in `promtail.yml`). To also ship Postgres/Redis/Typesense
logs, extend the `.*/(api|worker|web|admin)-[0-9]+$` regex accordingly.

## Sentry

Sentry is **not** a service in this stack. Error tracking is configured at the
application layer (Next.js `web` and `admin`). The Sentry DSNs are read from the
web/admin app environment variables (see the `apps/web` and `apps/admin`
Dockerfiles / env), and should be added to `.env.prod` there — not here.

## Files

- `prometheus.yml` — scrape config (self + node-exporter + cAdvisor)
- `loki.yml` — Loki single-binary config (filesystem TSDB, 31d retention)
- `promtail.yml` — Docker socket discovery → Loki
- `grafana/provisioning/datasources/datasources.yml` — Prometheus + Loki datasources
- `grafana/provisioning/dashboards/dashboards.yml` — dashboard provider (SokaLens folder)
- `grafana/provisioning/dashboards/node-exporter-full.json` — host metrics dashboard
- `grafana/provisioning/dashboards/cadvisor.json` — per-container metrics dashboard
