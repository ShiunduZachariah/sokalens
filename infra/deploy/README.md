# Deployment Boundary

Production deployment manifests, VPS bootstrap scripts, reverse-proxy config, backup jobs, and environment-specific deployment notes live here.

## Providers

- **API-Football (primary)** — fixtures/odds/predictions/live (Pro plan, quota-governed by the worker planner).
- **TheSportsDB (reference metadata)** — free-tier logos/badges/banners/squads/standings; `services/worker/internal/provider/thesportsdb`, key `123`, used by the daily `provider.thesportsdb.reference-sync` logo backfill job.
- **football-data.org (score history)** — wired via `services/worker/internal/provider/footballdata`; enable with `ENABLE_REAL_FOOTBALL_DATA_ORG_CALLS=true` + `FOOTBALL_DATA_ORG_API_KEY`.

## Deployments go through GitHub CI only

Production deployments are driven by the CI pipeline — never by direct SSH from a laptop.

- `.github/workflows/ci.yml` runs the path-filtered test/lint/build gates, then a `deploy` job on **green main pushes** that touch deployable surfaces (api/worker/web/admin/infra/contracts/security/ops).
- The deploy job rebuilds only the affected services (or the full set when infra/compose changes) and runs `infra/deploy/ci-deploy.sh` on the VPS as the `deploy` user:
  1. Fresh `tar` extract + atomic swap of `/home/deploy/sokalens` (preserving `.env.prod` and `fetchproviderpredictions-linux`),
  2. `~/dbtool-linux -mode up` for pending migrations,
  3. `docker compose ... up -d --build <services>`,
  4. validate, install, and reload the repository Caddy configuration, restoring its last-known-good copy if the release rolls back.
- The workflows reach the VPS only through `.github/actions/vps-ssh`, which reads every connection detail from repository secrets: `VPS_HOST`, `VPS_SSH_PORT`, `VPS_SSH_USER`, `VPS_SSH_PRIVATE_KEY` and `VPS_KNOWN_HOSTS` (the pinned host key). No address, port, user or key is written in the repository.

### Branch, merge, and deployment flow

Nothing is pushed straight to `main` (production) or `qa` (QA). Every change
arrives as a pull request.

1. Create one focused branch (`feat/*`, `fix/*`, `chore/*`, `docs/*`, `refactor/*`, `test/*`, or `perf/*`) and open a pull request into `qa`.
2. CI runs the path-filtered gates; the **CI gates** check summarises them.
3. A code owner (`.github/CODEOWNERS`) approves and the PR merges. Merging into `qa` deploys the QA environment (`qa-deploy.yml`).
4. Once QA checks out, a `qa` → `main` pull request goes through the same review. Promotions use a merge commit; feature PRs squash.
5. Green `main` CI deploys affected services, verifies the live public pages and every sitemap, then notifies IndexNow.

The ruleset in `.github/rulesets/protect-main-and-qa.json` enforces this: pull
request required, one code-owner approval with stale approvals dismissed, the
CI gates check green, no force-push and no deletion. Repository admins may
bypass only by merging a pull request, never by pushing. GitHub's native
auto-merge can be switched on per PR; it still waits for the approval.

Manual deploys are reserved for emergency rollbacks only.

### Verify the private API boundary after deployment

The production overlay requires Docker Compose 2.24.4 or newer and replaces API
port bindings with `127.0.0.1:8080`. Web/admin continue to reach `api:8080` on the
Docker network. Verify the running container's port bindings, host-local readiness,
and public prediction pages; an external connection to VPS port 8080 must fail.
Check GET and HEAD requests for root, nested, and encoded `.env` paths return 404
without file contents, following canonical-host redirects where present.

When a previous infrastructure release was blocked before deployment, a later
web-only rollout does not recreate the API container. Include the pending
infrastructure changes in a full CI rollout before declaring the boundary fixed.

## VPS production overlay (Contabo host: 8 vCPU / 24 GB / 290 GB)

Production shares this host with QA. `docs/operations/environments.md` is the
authoritative map of both — read it before changing a port or adding a stack.

- `docker-compose.prod.yml` — production overlay (resource limits, restart, log rotation) layered on `infra/docker/docker-compose.yml`.
- `.env.prod.example` — placeholder-only production environment template. Copy to `.env.prod`, fill in real secrets, and keep the file mode `0600`.
- `docker-compose.qa.yml` / `.env.qa.example` — the QA equivalents, run rootless under a separate user.

Run (same command the CI deploy job uses):

```bash
docker compose \
  -f infra/docker/docker-compose.yml \
  -f infra/deploy/docker-compose.prod.yml \
  --env-file .env.prod \
  --profile core --profile frontend --profile search \
  up -d
```

## Memory budget (24 GB host)

Ceilings, not reservations. The sum exceeds installed RAM deliberately: measured
usage is far below it, and the limits exist to contain a runaway process rather
than to partition the host. QA shares this host and is budgeted separately in
`docs/operations/environments.md`.

| Service | Limit | Measured 2026-09-16 | Purpose |
|---|---|---|---|
| postgres | 6 GB | 968 MB | source of truth (`shared_buffers=1536MB`) |
| worker | 1536 MB | 28 MB | Go + Python prediction engine |
| web | 1536 MB | 248 MB | Next.js public site |
| api | 1 GB | 23 MB | public/admin API |
| typesense | 1 GB | 86 MB | search |
| admin | 768 MB | 101 MB | Next.js admin |
| redis | 512 MB | 28 MB | cache/queue/lock (`maxmemory 128mb`) |
| observability | uncapped | ~1.1 GB | prometheus, grafana, loki, promtail, cadvisor, uptime |

These were sized for the retired 4 vCPU / 6 GB host and left unrevised through
the Contabo migration. Postgres capped at 1536 MB sat at 968 MB of that ceiling
and was repeatedly read as "memory pressure" while the host had ~20 GB free.

Keep API as a single replica initially; size PostgreSQL `max_connections` against the per-replica pool (API defaults to 20 open / 10 idle, worker 12 open / 6 idle). Scale by adding RAM first, then a Postgres read replica, then API replicas behind Cloudflare.

## Reverse proxy (Caddy)

`Caddyfile` routes the public hostnames to the localhost-only web/admin ports:

- `sokalens.com`, `www.sokalens.com` → `web` (`127.0.0.1:3000`)
- `hq.sokalens.com` → `admin` (`127.0.0.1:3001`)
- `qa.sokalens.com` → QA `web` (`127.0.0.1:13000`), behind basic auth
- `qa-admin.sokalens.com` → QA `admin` (`127.0.0.1:13001`), behind basic auth

The admin console is on `hq` rather than `admin` because the latter is on every
credential-stuffing wordlist. The old hostname is not redirected — a redirect
would hand the new name straight to the scanners the rename exists to shed — so
its DNS record is deleted outright.

Both QA hosts are proxied in Cloudflare, and both are single-label names by
necessity. Two constraints apply at once: Universal SSL covers only one label of
subdomain, so `admin.qa.sokalens.com` would fail TLS at the edge; and ufw
allows `:443` only from Cloudflare's ranges, so grey-clouding that host to fix
the certificate would instead make it unreachable. `qa-admin.sokalens.com`
satisfies both. Basic-auth credentials live in the password manager, never in
the repo — only the bcrypt hash is committed.

QA was previously served at `dev.sokalens.com` / `dev-admin.sokalens.com`. Those
Caddy blocks and their DNS records were removed on 2026-09-16 — they were a
second name for one environment, not a second environment. There are exactly
two: production and QA. See `docs/operations/environments.md`.

Install and activate once Cloudflare DNS is live (Full-strict requires a valid origin cert, which Caddy auto-provisions via Let's Encrypt after DNS resolves):

```bash
sudo apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt-get update && sudo apt-get install -y caddy
sudo cp ~/sokalens/infra/deploy/Caddyfile /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

## Backups to Cloudflare R2

`backup.sh` dumps PostgreSQL (`pg_dump`) and uploads a gzipped copy to the R2 bucket `sokalens-backups`. Prerequisites and the cron line are documented in the script header. R2 uses its own S3-compatible endpoint and does not depend on `sokalens.com` DNS, so backups can be enabled independently of the DNS/SSL blocker.
