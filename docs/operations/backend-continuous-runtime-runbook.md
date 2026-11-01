# Backend Continuous Runtime Runbook

Last updated: 2026-07-14

This runbook starts and verifies the API, PostgreSQL, Redis, recurring worker
scheduler, provider ingestion, and Python prediction engine without depending on
the web or admin applications.

## Provider mode

Use hybrid mode when both credentials are available:

- API-Football: daily reference data, yesterday/today/tomorrow fixtures, and
  today/tomorrow pre-match odds.
- football-data.org: accessible competition discovery and daily current-season
  match ingestion. This supplies finished history and acts as a fixture fallback.
  Future cup rounds whose participants are not resolved yet are logged and
  skipped until a later daily refresh supplies real teams.
- Python engine: reads normalized PostgreSQL rows only. It never calls a provider.

The default schedule is bounded:

- Provider rolling window: every `4h`.
- Prediction generation and settlement: every `1m`; upcoming generation is
  kickoff-ordered in batches of 500, while completed backfills enqueue an
  immediate fixture-specific regeneration job.
- Accuracy snapshots and provider reference catalogs: daily.
- API-Football daily reserve: 20 of 100 requests.
- API-Football minute reserve: 2 of 10 requests.

## Environment

Keep secrets only in an ignored `.env` file or the VPS secret manager. Never put
real tokens in `.env.example`, Compose YAML, source code, or Git history.

```env
# Use local during the VPS-style soak. Switch to production only after the
# production database/Redis/TLS/backup boundaries below are satisfied.
APP_ENV=local
APP_SECRET=<long-random-secret>

POSTGRES_DB=sports_prediction
POSTGRES_USER=<database-user>
POSTGRES_PASSWORD=<database-password>
DATABASE_URL=postgres://<encoded-user>:<encoded-password>@127.0.0.1:5433/sports_prediction?sslmode=disable
DATABASE_URL_DOCKER=postgres://<encoded-user>:<encoded-password>@postgres:5432/sports_prediction?sslmode=disable
REDIS_URL_DOCKER=redis://redis:6379/0

FOOTBALL_PROVIDER=hybrid
ENABLE_REAL_API_FOOTBALL_CALLS=true
ENABLE_MOCK_API_FOOTBALL_CALLS=false
API_FOOTBALL_KEY=<token>
ENABLE_REAL_FOOTBALL_DATA_ORG_CALLS=true
FOOTBALL_DATA_ORG_API_KEY=<token>

WORKER_POLL_INTERVAL=10s
PROVIDER_SCHEDULE_INTERVAL=4h
PREDICTION_SCHEDULE_INTERVAL=1m
WORKER_HEARTBEAT_MAX_AGE=10m
PROVIDER_DAILY_LIMIT=7500
PROVIDER_DAILY_RESERVE=1500
PROVIDER_MINUTE_LIMIT=450
PROVIDER_MINUTE_RESERVE=15
```

`DATABASE_URL` is used by host-run tools such as `dbtool`;
`DATABASE_URL_DOCKER` is used inside the Compose network. Percent-encode URI
reserved characters in usernames/passwords. Compose passes `POSTGRES_PASSWORD`
to PostgreSQL separately and never interpolates the raw value into a URI.
Production mode rejects the repository's development database credentials and
an unauthenticated Redis URL; use production secret-manager URLs (prefer TLS)
before changing `APP_ENV` to `production`.

For mock-only local work, use `FOOTBALL_PROVIDER=api-football`, disable both real
provider flags, and set `ENABLE_MOCK_API_FOOTBALL_CALLS=true`.

## Start or upgrade

Start the stateful dependencies first, apply migrations, then start the backend:

```powershell
docker compose --env-file .env -f infra/docker/docker-compose.yml --profile core up -d postgres redis mock-api-football
go run ./services/api/cmd/dbtool -env-file .env -mode up -migrations infra/db/migrations
docker compose --env-file .env -f infra/docker/docker-compose.yml build api worker
docker compose --env-file .env -f infra/docker/docker-compose.yml --profile core up -d api worker
```

`dbtool` loads the optional env file without overriding variables already present
in the process, so the same command works in PowerShell and Linux. Keep
VPS/backend runs on `--profile core`; do not use `--profile all` on an
internet-facing host because it also publishes development/auxiliary services.
The API, worker, PostgreSQL, Redis, and local mock provider use
`restart: unless-stopped`. API and worker containers have functional healthchecks.

## Verify runtime health

```powershell
docker compose --env-file .env -f infra/docker/docker-compose.yml --profile core ps
Invoke-RestMethod http://127.0.0.1:8080/readyz
docker compose --env-file .env -f infra/docker/docker-compose.yml logs --since 15m --no-color worker
```

Healthy worker logs include:

- `schedule job ensured`
- `job started` and `job completed`
- `provider normalization completed` with provider, endpoint, job ID, and counts
- `provider record skipped` with provider match ID and reason when a legitimate
  unresolved fixture cannot yet be normalized
- `prediction engine python summary` with processed, written, failed, and skipped counts

The worker heartbeat reports health only after scheduling and queue processing
complete without infrastructure errors. Three consecutive infrastructure-level
iteration failures make the process exit so `restart: unless-stopped` can recover
it. Provider-level failures are recorded on their jobs and do not stop the other
queues or provider. API-Football `Retry-After`/reserve waits defer the affected
job in PostgreSQL rather than sleeping the shared worker. The daily reserve also
restores its spent count from `provider_request_logs` after a restart, so do not
bypass a preflight deferral merely because the last stored response header looks
less restrictive than the local request ledger.

## Verify queues and providers

```powershell
$sql = @'
SELECT status, count(*) FROM jobs GROUP BY status ORDER BY status;
SELECT id, job_key, queue, status, attempts, run_after, last_error
FROM jobs
WHERE status IN ('queued', 'running', 'failed')
ORDER BY id DESC
LIMIT 100;
SELECT provider, endpoint, status_code, quota_cost, job_id, error_category, requested_at
FROM provider_request_logs
ORDER BY id DESC
LIMIT 50;
SELECT provider, daily_limit, daily_remaining, minute_limit, minute_remaining, updated_at
FROM provider_rate_limits
ORDER BY provider;
'@
docker compose --env-file .env -f infra/docker/docker-compose.yml exec -T postgres `
  psql -U sports -d sports_prediction -P pager=off -c $sql
```

Old failed rows are retained as audit history and do not block new bucketed jobs.
Judge a deployment by new job IDs and timestamps rather than deleting old failures.
Replace the example `sports`/`sports_prediction` psql values when the VPS uses
custom `POSTGRES_USER` or `POSTGRES_DB` settings.

## Verify prediction generation

```powershell
$sql = @'
SELECT f.id, f.kickoff_at, l.name AS league, ht.name AS home, at.name AS away,
       count(ep.id) AS current_markets
FROM fixtures f
JOIN leagues l ON l.id = f.league_id
JOIN teams ht ON ht.id = f.home_team_id
JOIN teams at ON at.id = f.away_team_id
JOIN engine_predictions ep ON ep.fixture_id = f.id AND ep.superseded_at IS NULL
WHERE f.kickoff_at >= now()
GROUP BY f.id, f.kickoff_at, l.name, ht.name, at.name
ORDER BY f.kickoff_at
LIMIT 20;
'@
docker compose --env-file .env -f infra/docker/docker-compose.yml exec -T postgres `
  psql -U sports -d sports_prediction -P pager=off -c $sql
```

Then query a returned fixture through the API:

```powershell
Invoke-RestMethod http://127.0.0.1:8080/v1/matches/<fixture-id>
Invoke-RestMethod http://127.0.0.1:8080/v1/predictions/today
```

`/v1/predictions/today` is intentionally restricted to the current
Africa/Nairobi day. A future published prediction can be verified immediately
through `/v1/matches/<fixture-id>` even when today's list is empty.

## Restart and stale-job recovery

Scheduled idempotency keys include cadence buckets and deduplicate completed
slots, so restarting a worker does not repeat provider calls for the current
slot. A running job whose lease is older than 15 minutes is reclaimed when it
has attempts left or marked failed when attempts are exhausted.

```powershell
docker compose --env-file .env -f infra/docker/docker-compose.yml restart worker
docker compose --env-file .env -f infra/docker/docker-compose.yml logs --since 5m --no-color worker
```

## VPS boundaries

- Do not publish PostgreSQL, Redis, or Docker daemon ports to the public internet.
- Use only the `core` profile on a VPS unless every additional port is explicitly
  firewalled and its credentials are replaced.
- Put the API behind TLS and a firewall/reverse proxy.
- Set `APP_ENV=production` only with non-development PostgreSQL credentials and
  an authenticated production Redis URL; production rejects local defaults.
- Configure Docker to start at boot and retain the Compose restart policies.
- Send logs and container-health alerts to an external monitor.
- Replace the current placeholder backup container with encrypted off-host
  PostgreSQL backups and complete a restore drill before production sign-off.
